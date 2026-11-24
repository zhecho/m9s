package imap_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"

	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
)

const bulgarianMsg = "From: Bob <bob@example.com>\r\nTo: me@example.com\r\nSubject: second\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nЗдравей\r\n"

// mirrorWithMail returns a mirror connection to a server whose folder A holds
// testMsg (UID 1, unread) and bulgarianMsg (UID 2, read).
func mirrorWithMail(t *testing.T) (imapadapter.Mirror, imapadapter.Client) {
	t.Helper()
	c := startMemServer(t)
	ctx := context.Background()
	if _, err := c.Append(ctx, "A", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(ctx, "A", []byte(bulgarianMsg), []imap.Flag{imap.FlagSeen}); err != nil {
		t.Fatal(err)
	}
	return c.(imapadapter.Mirror), c
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func TestSnapshot_listsEveryUIDWithItsFlags(t *testing.T) {
	m, _ := mirrorWithMail(t)
	validity, flags, err := m.Snapshot(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if validity == 0 {
		t.Error("uidvalidity = 0; a renumbered folder could not be noticed")
	}
	if len(flags) != 2 {
		t.Fatalf("snapshot holds %d messages, want 2: %v", len(flags), flags)
	}
	if hasFlag(flags[1], `\Seen`) || !hasFlag(flags[2], `\Seen`) {
		t.Errorf("flags = %v, want only UID 2 seen", flags)
	}
}

func TestSnapshot_emptyFolder(t *testing.T) {
	m, _ := mirrorWithMail(t)
	_, flags, err := m.Snapshot(context.Background(), "B")
	if err != nil || len(flags) != 0 {
		t.Errorf("Snapshot(B) = %v, %v; want empty and no error", flags, err)
	}
}

func TestFetchEnvelopes_onlyTheAskedUIDs(t *testing.T) {
	m, _ := mirrorWithMail(t)
	envs, err := m.FetchEnvelopes(context.Background(), "A", []uint32{2})
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].UID != 2 || envs[0].Subject != "second" {
		t.Fatalf("got %+v, want only UID 2 \"second\"", envs)
	}
	if envs[0].FromDisplay() != "Bob" {
		t.Errorf("from = %q", envs[0].FromDisplay())
	}
}

// fetchRaw collects what FetchRaw hands out for uids.
func fetchRaw(t *testing.T, m imapadapter.Mirror, folder string, uids ...uint32) map[uint32]string {
	t.Helper()
	got := map[uint32]string{}
	err := m.FetchRaw(context.Background(), folder, uids, func(uid uint32, raw []byte) error {
		got[uid] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFetchRaw_returnsWholeMessagesWithoutMarkingThemRead(t *testing.T) {
	m, c := mirrorWithMail(t)
	got := fetchRaw(t, m, "A", 1, 2)
	if got[1] != testMsg || got[2] != bulgarianMsg {
		t.Errorf("raw = %v, want both messages exactly as stored", got)
	}
	envs, _ := c.FetchHeaders(context.Background(), "A", 1, 50)
	if len(envs) != 2 || !envs[0].IsUnread() {
		t.Error("copying the message marked it as read on the server")
	}
}

func TestFetchRaw_skipsMessagesThatNoLongerExist(t *testing.T) {
	m, _ := mirrorWithMail(t)
	got := fetchRaw(t, m, "A", 99, 2)
	if len(got) != 1 || got[2] != bulgarianMsg {
		t.Errorf("got %v, want only UID 2", got)
	}
}

func TestFetchRaw_callbackErrorIsReturnedAndConnectionStaysUsable(t *testing.T) {
	m, _ := mirrorWithMail(t)
	diskFull := errors.New("disk full")
	calls := 0
	err := m.FetchRaw(context.Background(), "A", []uint32{1, 2}, func(uint32, []byte) error {
		calls++
		return diskFull
	})
	if !errors.Is(err, diskFull) || calls != 1 {
		t.Errorf("err = %v after %d calls, want the callback's error after 1", err, calls)
	}
	if got := fetchRaw(t, m, "A", 1); got[1] != testMsg {
		t.Error("the connection is unusable after a failed save")
	}
}

func TestParseMessage_readsARawMessageLikeAFetchedOne(t *testing.T) {
	m, c := mirrorWithMail(t)
	ctx := context.Background()
	raw := fetchRaw(t, m, "A", 2)[2]
	envs, _ := m.FetchEnvelopes(ctx, "A", []uint32{2})
	local := mail.ParseMessage(envs[0], []byte(raw))
	fetched, err := c.FetchBody(ctx, "A", 2)
	if err != nil {
		t.Fatal(err)
	}
	if local.Envelope.UID != 2 || !strings.Contains(local.TextBody, "Здравей") {
		t.Errorf("parsed locally: uid %d body %q", local.Envelope.UID, local.TextBody)
	}
	if local.TextBody != fetched.TextBody {
		t.Errorf("local body %q differs from the fetched %q", local.TextBody, fetched.TextBody)
	}
}

const pdfAttachMsg = "From: a@example.com\r\nTo: b@example.com\r\nSubject: bill\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain\r\n\r\n" +
	"see attached\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=bill.pdf\r\n\r\n" +
	"JVBERg==\r\n" +
	"--MIX--\r\n"

const inlineImageMsg = "From: a@example.com\r\nTo: b@example.com\r\nSubject: logo\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=REL\r\n\r\n" +
	"--REL\r\n" +
	"Content-Type: text/html\r\n\r\n" +
	"<img src=\"cid:logo\">\r\n" +
	"--REL\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <logo>\r\n" +
	"Content-Disposition: inline; filename=logo.png\r\n\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--REL--\r\n"

func TestFetchEnvelopes_attachmentSign(t *testing.T) {
	c := startMemServer(t)
	ctx := context.Background()
	for _, raw := range []string{testMsg, pdfAttachMsg, inlineImageMsg, pdfNoDispMsg, cidNoDispMsg} {
		if _, err := c.Append(ctx, "A", []byte(raw), nil); err != nil {
			t.Fatal(err)
		}
	}
	envs, err := c.(imapadapter.Mirror).FetchEnvelopes(ctx, "A", []uint32{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32]bool{}
	for _, e := range envs {
		got[e.UID] = e.HasAttach
	}
	if got[1] || !got[2] || got[3] || !got[4] || got[5] {
		t.Errorf("signs = %v, want UIDs 2 and 4 (the PDFs) marked, not the pictures", got)
	}
}

const pdfNoDispMsg = "From: a@example.com\r\nTo: b@example.com\r\nSubject: bill2\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain\r\n\r\n" +
	"see attached\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf; name=bill.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\n" +
	"JVBERg==\r\n" +
	"--MIX--\r\n"

const cidNoDispMsg = "From: a@example.com\r\nTo: b@example.com\r\nSubject: logo2\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=REL\r\n\r\n" +
	"--REL\r\n" +
	"Content-Type: text/html\r\n\r\n" +
	"<img src=\"cid:logo\">\r\n" +
	"--REL\r\n" +
	"Content-Type: image/png; name=logo.png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <logo>\r\n\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--REL--\r\n"
