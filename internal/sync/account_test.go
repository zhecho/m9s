package sync_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

func TestEngineImplementsAccount(t *testing.T) {
	var _ msync.Account = (*msync.Engine)(nil)
	var _ msync.ServerSearch = (*msync.Engine)(nil)
}

func TestEngineSearchReturnsNewestEnvelopes(t *testing.T) {
	mc := &mockClient{searchResp: []uint32{3, 1, 2}}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)

	envs, err := eng.Search(context.Background(), "INBOX", "hello world")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var ids []string
	for _, e := range envs {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []string{"3", "2", "1"}) {
		t.Errorf("ids = %v, want [3 2 1]", ids)
	}
	if mc.searchFolder != "INBOX" || !slices.Equal(mc.searchText, []string{"hello", "world"}) {
		t.Errorf("searched %q for %v", mc.searchFolder, mc.searchText)
	}
	if mc.fetchEnvFolder != "INBOX" {
		t.Errorf("envelopes fetched from %q", mc.fetchEnvFolder)
	}
}

func TestEngineSearchFetchesOnlyTheNewestListSize(t *testing.T) {
	var uids []uint32
	for u := uint32(1); u <= store.ListSize+10; u++ {
		uids = append(uids, u)
	}
	mc := &mockClient{searchResp: uids}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)

	envs, err := eng.Search(context.Background(), "INBOX", "x")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(mc.fetchEnvUIDs) != store.ListSize || len(envs) != store.ListSize {
		t.Fatalf("fetched %d, returned %d; want %d", len(mc.fetchEnvUIDs), len(envs), store.ListSize)
	}
	if envs[0].ID != strconv.Itoa(store.ListSize+10) || envs[len(envs)-1].ID != "11" {
		t.Errorf("first %s last %s, want the newest %d", envs[0].ID, envs[len(envs)-1].ID, store.ListSize)
	}
}

func TestEngineSearchNoMatches(t *testing.T) {
	mc := &mockClient{}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)
	envs, err := eng.Search(context.Background(), "INBOX", "nothing")
	if err != nil || len(envs) != 0 {
		t.Errorf("Search = %v, %v; want nothing", envs, err)
	}
	if mc.fetchEnvUIDs != nil {
		t.Error("no matches must not fetch envelopes")
	}
}

// passwordSender records the password it was given and fails with err.
type passwordSender struct {
	password string
	draft    smtpadapter.Draft
	err      error
}

func (s *passwordSender) Send(password string, d smtpadapter.Draft) ([]byte, error) {
	s.password, s.draft = password, d
	if s.err != nil {
		return nil, s.err
	}
	return []byte("sent:" + d.Subject), nil
}

func TestEngineSendSMTPPassword(t *testing.T) {
	s := &passwordSender{}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), &mockClient{}, &mockClient{})
	eng.SetSender(s)
	eng.SetSMTPPassword(" smtp pw ")

	msg, err := eng.Send(context.Background(), smtpadapter.Draft{Subject: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s.password != " smtp pw " || s.draft.Subject != "hi" || string(msg) != "sent:hi" {
		t.Errorf("sent with %q draft %+v → %q", s.password, s.draft, msg)
	}

	s.err = fmt.Errorf("%w: 535 bad credentials", smtpadapter.ErrAuth)
	if _, err := eng.Send(context.Background(), smtpadapter.Draft{}); !errors.Is(err, smtpadapter.ErrAuth) {
		t.Errorf("err = %v, want wrapped smtp ErrAuth", err)
	}
}

func TestEngineSendNoSMTPPassword(t *testing.T) {
	s := &passwordSender{}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), &mockClient{}, &mockClient{})
	eng.SetSender(s)
	eng.SetSMTPPassword("")
	_, err := eng.Send(context.Background(), smtpadapter.Draft{})
	if !errors.Is(err, msync.ErrNoSMTPPassword) || err.Error() != "no SMTP password for test" {
		t.Errorf("err = %v, want 'no SMTP password for test'", err)
	}
	if s.password != "" || s.draft.Subject != "" {
		t.Error("nothing may be sent without a password")
	}
}

// searchEnvelope is what the mock's FetchEnvelopes hands back for uid.
func searchEnvelope(uid uint32) *mail.Envelope {
	return &mail.Envelope{ID: strconv.FormatUint(uint64(uid), 10), UID: uid, SortKey: int64(uid)}
}

// An id that is not a UID must never reach the server: a wrong UID would
// delete or move another message.
func TestDestructiveOpsRejectBadIDs(t *testing.T) {
	ops := map[string]func(*msync.Engine, string) error{
		"MoveToTrash": func(e *msync.Engine, id string) error {
			return e.MoveToTrash(context.Background(), "INBOX", id)
		},
		"PermanentDelete": func(e *msync.Engine, id string) error {
			return e.PermanentDelete(context.Background(), "Trash", id)
		},
		"DeleteDraftCopy": func(e *msync.Engine, id string) error {
			return e.DeleteDraftCopy(context.Background(), id)
		},
		"TrashDraftCopy": func(e *msync.Engine, id string) error {
			return e.TrashDraftCopy(context.Background(), id)
		},
		"SetFlags": func(e *msync.Engine, id string) error {
			return e.SetFlags(context.Background(), "INBOX", id, []string{`\Seen`}, true)
		},
		"FetchBody": func(e *msync.Engine, id string) error {
			_, err := e.FetchBody(context.Background(), "INBOX", id)
			return err
		},
	}
	for name, op := range ops {
		for _, id := range []string{"", "0", "abc", "-1", " 7", "4294967296"} {
			t.Run(fmt.Sprintf("%s/%q", name, id), func(t *testing.T) {
				mc := &mockClient{foldersResp: []*mail.Folder{
					{Name: "INBOX", Delimiter: "/"},
					{Name: "Drafts", Delimiter: "/", Attributes: []string{`\Drafts`}},
					{Name: "Trash", Delimiter: "/", Attributes: []string{`\Trash`}},
				}}
				eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)
				if _, err := eng.Folders(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := op(eng, id); err == nil {
					t.Error("want an error for a bad id")
				}
				if mc.moveCalledSrc != "" || mc.expungeCalled || mc.storeCalls != 0 || mc.fetchBodyCalls != 0 ||
					mc.appendCount() != 0 || mc.expungeAllFolder != "" {
					t.Errorf("the server was called: move %q expunge %v store %d fetchBody %d",
						mc.moveCalledSrc, mc.expungeCalled, mc.storeCalls, mc.fetchBodyCalls)
				}
			})
		}
	}
}
