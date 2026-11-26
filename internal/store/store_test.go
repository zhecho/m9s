package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

func env(uid uint32, flags ...string) *mail.Envelope {
	return &mail.Envelope{
		ID: strconv.FormatUint(uint64(uid), 10), SortKey: int64(uid), // as the IMAP adapter fills them
		UID: uid, Subject: fmt.Sprintf("msg %d", uid), Flags: flags,
		From: []mail.Address{{Name: "Боб", Email: "bob@x.io"}}, // Bulgarian must survive
		Date: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(uid) * time.Hour),
	}
}

// envs returns envelopes for the given UIDs, in that order.
func envs(uids ...uint32) []*mail.Envelope {
	out := make([]*mail.Envelope, len(uids))
	for i, u := range uids {
		out[i] = env(u)
	}
	return out
}

func uidsOf(list []*mail.Envelope) []uint32 {
	out := []uint32{}
	for _, e := range list {
		out = append(out, e.UID)
	}
	return out
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mail")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// --- what the UI shows ---

func TestEnvelopes_unknownFolderIsNotKnown(t *testing.T) {
	s := NewMemory()
	if _, ok := s.Envelopes("a", "INBOX"); ok {
		t.Error("an empty store claimed to know a folder")
	}
}

func TestEnvelopes_returnsTheListInServerOrder(t *testing.T) {
	s := NewMemory()
	want := []*mail.Envelope{env(3), env(7, `\Seen`, `\Flagged`)}
	must(t, s.PutEnvelopes("a", "INBOX", want))

	got, ok := s.Envelopes("a", "INBOX")
	if !ok || len(got) != 2 {
		t.Fatalf("got %d envelopes, known=%v", len(got), ok)
	}
	for i := range want {
		if !got[i].Date.Equal(want[i].Date) {
			t.Errorf("envelope %d: date %v, want %v", i, got[i].Date, want[i].Date)
		}
		got[i].Date, want[i].Date = time.Time{}, time.Time{}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("envelope %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestEnvelopes_keepsAccountsAndFoldersApart(t *testing.T) {
	s := NewMemory()
	must(t, s.PutEnvelopes("a", "INBOX", envs(1)))
	if _, ok := s.Envelopes("b", "INBOX"); ok {
		t.Error("account b got account a's INBOX")
	}
	if _, ok := s.Envelopes("a", "Sent"); ok {
		t.Error("Sent got INBOX's messages")
	}
}

func TestEnvelopes_emptyFolderIsKnownAndEmpty(t *testing.T) {
	s := NewMemory()
	must(t, s.PutEnvelopes("a", "Trash", nil))
	if got, ok := s.Envelopes("a", "Trash"); !ok || len(got) != 0 {
		t.Errorf("got %d envelopes, known=%v; want a known empty folder", len(got), ok)
	}
}

func TestEnvelopes_showsOnlyTheNewestPageOfAFullCopy(t *testing.T) {
	s := NewMemory()
	var all []uint32
	for u := uint32(1); u <= ListSize+20; u++ {
		all = append(all, u)
	}
	must(t, s.AddMessages("a", "INBOX", envs(all...))) // the background copy holds everything

	got, ok := s.Envelopes("a", "INBOX")
	if !ok || len(got) != ListSize {
		t.Fatalf("got %d envelopes, known=%v; want the newest %d", len(got), ok, ListSize)
	}
	if got[0].UID != 21 || got[len(got)-1].UID != ListSize+20 {
		t.Errorf("list runs %d..%d, want 21..%d as the server lists it", got[0].UID, got[len(got)-1].UID, ListSize+20)
	}
}

func TestPutEnvelopes_dropsMessagesDeletedOnTheServer(t *testing.T) {
	s := NewMemory()
	must(t, s.PutEnvelopes("a", "INBOX", envs(3, 5, 7)))
	must(t, s.PutEnvelopes("a", "INBOX", envs(3, 7))) // 5 was deleted elsewhere

	got, _ := s.Envelopes("a", "INBOX")
	if !reflect.DeepEqual(uidsOf(got), []uint32{3, 7}) {
		t.Errorf("list = %v, want [3 7]", uidsOf(got))
	}
}

func TestPutEnvelopes_fullPageLeavesOlderMailAlone(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("a", "INBOX", envs(1, 2, 3)))
	var page []uint32
	for u := uint32(100); u < 100+ListSize; u++ {
		page = append(page, u)
	}
	must(t, s.PutEnvelopes("a", "INBOX", envs(page...))) // the list only covers the newest page

	_, flags, err := s.FolderState("a", "INBOX")
	must(t, err)
	if len(flags) != ListSize+3 {
		t.Errorf("%d messages stored, want the page plus the 3 older ones", len(flags))
	}
}

func TestPutEnvelopes_updatesFlagsAndKeepsTheBody(t *testing.T) {
	s, _ := open(t)
	must(t, s.PutEnvelopes("a", "INBOX", envs(7)))
	must(t, s.PutBody("a", "INBOX", "7", []byte("raw")))
	must(t, s.PutEnvelopes("a", "INBOX", []*mail.Envelope{env(7, `\Seen`)})) // read meanwhile

	got, _ := s.Envelopes("a", "INBOX")
	if got[0].IsUnread() {
		t.Error("the message still shows as unread")
	}
	if _, ok := s.Body("a", "INBOX", "7"); !ok {
		t.Error("refreshing the list threw the downloaded message away")
	}
}

func TestEnvelope_singleMessage(t *testing.T) {
	s := NewMemory()
	must(t, s.PutEnvelopes("a", "INBOX", envs(3, 7)))
	got, ok := s.Envelope("a", "INBOX", "7")
	if !ok || got.UID != 7 || got.Subject != "msg 7" {
		t.Errorf("Envelope(7) = %+v, %v", got, ok)
	}
	if _, ok := s.Envelope("a", "INBOX", "99"); ok {
		t.Error("a message that is not stored was returned")
	}
}

// --- surviving a restart ---

func TestOpen_everythingSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	s, err := Open(dir)
	must(t, err)
	must(t, s.PutEnvelopes("a", "INBOX", envs(3, 7)))
	must(t, s.PutBody("a", "INBOX", "7", []byte("Subject: hi\r\n\r\nbody")))
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: &mail.MailboxStatus{Messages: 128, Unseen: 3}},
		{Name: "Work/Заявки", Delimiter: "/", Attributes: []string{`\HasNoChildren`}},
	}
	must(t, s.PutFolders("a", folders))
	must(t, s.Close())

	s, err = Open(dir)
	must(t, err)
	defer s.Close()
	if got, ok := s.Envelopes("a", "INBOX"); !ok || !reflect.DeepEqual(uidsOf(got), []uint32{3, 7}) {
		t.Errorf("list after restart = %v, known=%v", uidsOf(got), ok)
	}
	if raw, ok := s.Body("a", "INBOX", "7"); !ok || string(raw) != "Subject: hi\r\n\r\nbody" {
		t.Errorf("body after restart = %q, %v", raw, ok)
	}
	if got := s.Folders("a"); !reflect.DeepEqual(got, folders) {
		t.Errorf("folders after restart = %+v", got)
	}
	if s.Folders("b") != nil {
		t.Error("account b got account a's folders")
	}
}

func TestFolders_callerGetsItsOwnCopy(t *testing.T) {
	s := NewMemory()
	must(t, s.PutFolders("a", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}))
	s.Folders("a")[0].Name = "changed by the UI"
	if got := s.Folders("a")[0].Name; got != "INBOX" {
		t.Errorf("stored folder name = %q; a caller's change leaked into the store", got)
	}
}

func TestOpen_filesArePrivate(t *testing.T) {
	s, dir := open(t)
	must(t, s.PutEnvelopes("a", "Work/Заявки", envs(7)))
	must(t, s.PutBody("a", "Work/Заявки", "7", []byte("raw")))
	must(t, filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %o; mail must not be readable by other users", p, perm)
		}
		return nil
	}))
}

func TestOpen_unreadableIndexIsRebuilt(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "index.db"), []byte("this is not a database"), 0o600))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("a damaged index must be rebuilt, not fatal: %v", err)
	}
	defer s.Close()
	must(t, s.PutEnvelopes("a", "INBOX", envs(1)))
}

func TestDefaultDir_honoursXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
	if got := DefaultDir(); got != "/tmp/xdg/m9s/mail" {
		t.Errorf("DefaultDir() = %q", got)
	}
}

func TestNilStore_isAnEmptyStore(t *testing.T) {
	var s *Store
	if _, ok := s.Envelopes("a", "INBOX"); ok {
		t.Error("nil store returned envelopes")
	}
	if _, ok := s.Body("a", "INBOX", "1"); ok {
		t.Error("nil store returned a body")
	}
	if _, ok := s.Envelope("a", "INBOX", "1"); ok {
		t.Error("nil store returned an envelope")
	}
	if s.Folders("a") != nil {
		t.Error("nil store returned folders")
	}
	must(t, s.PutEnvelopes("a", "INBOX", envs(1)))
	must(t, s.PutFolders("a", nil))
	must(t, s.Close())
}

// --- message content ---

func TestBody_isStoredAsOneEMLFilePerMessage(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("a", "Work/Заявки", envs(7)))
	must(t, s.PutBody("a", "Work/Заявки", "7", []byte("raw message")))

	matches, _ := filepath.Glob(filepath.Join(dir, "a", "*", "7.eml"))
	if len(matches) != 1 {
		t.Fatalf("found %v, want exactly one 7.eml inside the account's folder directory", matches)
	}
	if data, _ := os.ReadFile(matches[0]); string(data) != "raw message" {
		t.Errorf("file holds %q, want the raw message untouched", data)
	}
}

func TestBody_missingUntilDownloaded(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(7)))
	if _, ok := s.Body("a", "INBOX", "7"); ok {
		t.Error("a body was returned before anything was downloaded")
	}
}

func TestBody_fileLostOutsideM9sIsDownloadedAgain(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(7)))
	must(t, s.PutBody("a", "INBOX", "7", []byte("raw")))
	must(t, os.RemoveAll(filepath.Join(dir, "a")))

	if _, ok := s.Body("a", "INBOX", "7"); ok {
		t.Fatal("a body was returned although its file is gone")
	}
	missing, err := s.MissingBodies("a", "INBOX")
	must(t, err)
	if !reflect.DeepEqual(missing, []string{"7"}) {
		t.Errorf("missing bodies = %v, want [7] so it is fetched again", missing)
	}
}

func TestMissingBodies_newestFirst(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(3, 9, 5)))
	must(t, s.PutBody("a", "INBOX", "9", []byte("raw")))
	missing, err := s.MissingBodies("a", "INBOX")
	must(t, err)
	if !reflect.DeepEqual(missing, []string{"5", "3"}) {
		t.Errorf("missing bodies = %v, want [5 3]", missing)
	}
}

// --- what the background copy needs ---

func TestFolderState_reportsStoredMessagesAndFlags(t *testing.T) {
	s := NewMemory()
	must(t, s.ResetFolder("a", "INBOX", 42))
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{env(3), env(7, `\Seen`)}))

	validity, flags, err := s.FolderState("a", "INBOX")
	must(t, err)
	if validity != 42 {
		t.Errorf("uidvalidity = %d, want 42", validity)
	}
	want := map[string][]string{"3": nil, "7": {`\Seen`}}
	if len(flags) != 2 || len(flags["3"]) != 0 || !reflect.DeepEqual(flags["7"], want["7"]) {
		t.Errorf("flags = %v, want %v", flags, want)
	}
}

func TestResetFolder_dropsMessagesAndFiles(t *testing.T) {
	s, dir := open(t)
	must(t, s.ResetFolder("a", "INBOX", 1))
	must(t, s.AddMessages("a", "INBOX", envs(7)))
	must(t, s.PutBody("a", "INBOX", "7", []byte("raw")))
	must(t, s.AddMessages("a", "Sent", envs(7)))
	must(t, s.PutBody("a", "Sent", "7", []byte("sent")))

	must(t, s.ResetFolder("a", "INBOX", 2)) // the server renumbered the folder

	if _, flags, _ := s.FolderState("a", "INBOX"); len(flags) != 0 {
		t.Errorf("%d messages survived the reset", len(flags))
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "a", "*", "7.eml")); len(matches) != 1 {
		t.Errorf("message files after reset: %v, want only Sent's", matches)
	}
	if raw, ok := s.Body("a", "Sent", "7"); !ok || string(raw) != "sent" {
		t.Error("resetting INBOX damaged Sent")
	}
}

func TestSetUIDValidity_keepsWhatIsStored(t *testing.T) {
	s := NewMemory()
	must(t, s.PutEnvelopes("a", "INBOX", envs(7))) // listed by the UI before the first copy pass
	must(t, s.SetUIDValidity("a", "INBOX", 42))
	validity, flags, _ := s.FolderState("a", "INBOX")
	if validity != 42 || len(flags) != 1 {
		t.Errorf("validity %d with %d messages, want 42 with the message kept", validity, len(flags))
	}
}

func TestRemoveMessages_dropsRowsAndFiles(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(3, 7)))
	must(t, s.PutBody("a", "INBOX", "3", []byte("raw3")))
	must(t, s.PutBody("a", "INBOX", "7", []byte("raw7")))

	must(t, s.RemoveMessages("a", "INBOX", []string{"3"}))

	if _, ok := s.Envelope("a", "INBOX", "3"); ok {
		t.Error("the removed message is still listed")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "a", "*", "*.eml")); len(matches) != 1 {
		t.Errorf("message files = %v, want only 7.eml", matches)
	}
}

func TestSetFlags_changesWhatTheListShows(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("a", "INBOX", envs(7)))
	must(t, s.SetFlags("a", "INBOX", map[string][]string{"7": {`\Seen`}}))
	got, _ := s.Envelopes("a", "INBOX")
	if got[0].IsUnread() {
		t.Error("the message still shows as unread")
	}
}

func TestPruneFolders_dropsFoldersGoneFromTheServer(t *testing.T) {
	s, dir := open(t)
	for _, f := range []string{"INBOX", "Old"} {
		must(t, s.AddMessages("a", f, envs(7)))
		must(t, s.PutBody("a", f, "7", []byte("raw")))
	}
	must(t, s.AddMessages("b", "Old", envs(1)))

	must(t, s.PruneFolders("a", []string{"INBOX"}))

	if _, ok := s.Envelopes("a", "Old"); ok {
		t.Error("the deleted folder is still known")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "a", "*", "7.eml")); len(matches) != 1 {
		t.Errorf("message files = %v, want only INBOX's", matches)
	}
	if _, ok := s.Envelopes("a", "INBOX"); !ok {
		t.Error("INBOX was dropped too")
	}
	if _, ok := s.Envelopes("b", "Old"); !ok {
		t.Error("another account's folder of the same name was dropped")
	}
}

func TestCounts_messagesAndDownloadedBodies(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(1, 2, 3)))
	must(t, s.AddMessages("a", "Sent", envs(1)))
	must(t, s.AddMessages("b", "INBOX", envs(1)))
	must(t, s.PutBody("a", "INBOX", "2", []byte("raw")))
	messages, bodies := s.Counts("a")
	if messages != 4 || bodies != 1 {
		t.Errorf("Counts = %d messages, %d bodies; want 4 and 1", messages, bodies)
	}
}

func TestUpsert_envelopeWithoutIDIsKeyedByItsUID(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{{UID: 9, Subject: "bare"}}))
	got, ok := s.Envelope("a", "INBOX", "9")
	if !ok || got.ID != "9" || got.SortKey != 9 {
		t.Errorf("Envelope(9) = %+v, %v; want ID 9 and SortKey 9", got, ok)
	}
}

// --- Gmail: shared bodies, threads, cursor ---

func gmailEnv(id, thread string, sortKey int64, flags ...string) *mail.Envelope {
	return &mail.Envelope{ID: id, ThreadID: thread, SortKey: sortKey, Subject: "msg " + id, Flags: flags}
}

func TestSharedBodyVisibleFromEveryLabel(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.AddMessages("acct", "Work", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.PutBody("acct", "", "a", []byte("raw a")))

	for _, f := range []string{"INBOX", "Work"} {
		missing, err := s.MissingBodies("acct", f)
		must(t, err)
		if len(missing) != 0 {
			t.Errorf("%s: missing bodies = %v, want none", f, missing)
		}
	}
	if raw, ok := s.Body("acct", "", "a"); !ok || string(raw) != "raw a" {
		t.Errorf("Body = %q, %v", raw, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "acct", "a.eml")); err != nil {
		t.Errorf("shared body not at <account>/<id>.eml: %v", err)
	}
	if _, bodies := s.Counts("acct"); bodies != 2 {
		t.Errorf("bodies = %d, want both rows marked downloaded", bodies)
	}
}

func TestSharedBodySurvivesRemoveFromOneLabel(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.AddMessages("acct", "Work", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("b", "u", 2)}))
	must(t, s.PutBody("acct", "", "a", []byte("raw a")))
	must(t, s.PutBody("acct", "", "b", []byte("raw b")))

	must(t, s.RemoveMessages("acct", "Work", []string{"a"}))
	if _, ok := s.Body("acct", "", "a"); !ok {
		t.Fatal("removing the message from one label lost its body")
	}
	must(t, s.PruneSharedBodies("acct"))
	if _, ok := s.Body("acct", "", "a"); !ok {
		t.Fatal("pruning dropped a body INBOX still references")
	}

	must(t, s.RemoveMessages("acct", "INBOX", []string{"a"}))
	if _, err := os.Stat(filepath.Join(dir, "acct", "a.eml")); err != nil {
		t.Fatalf("RemoveMessages deleted a shared body itself: %v", err)
	}
	must(t, s.PruneSharedBodies("acct"))
	if _, err := os.Stat(filepath.Join(dir, "acct", "a.eml")); !os.IsNotExist(err) {
		t.Errorf("a.eml still on disk after prune: %v", err)
	}
	if _, ok := s.Body("acct", "", "b"); !ok {
		t.Error("prune dropped a body still referenced")
	}
}

func TestThreadsGroupsByThreadID(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{
		gmailEnv("m1", "t1", 10),          // older, unread
		gmailEnv("m2", "t1", 30, `\Seen`), // newer
		gmailEnv("m3", "t2", 20, `\Seen`),
	}))
	got := s.Threads("acct", "INBOX")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 conversations", len(got))
	}
	// Like Envelopes: oldest first, the newest conversation last.
	if got[0].ID != "m3" || got[0].ThreadCount != 1 {
		t.Errorf("row 0 = %s (count %d), want m3 alone", got[0].ID, got[0].ThreadCount)
	}
	t1 := got[1]
	if t1.ID != "m2" || t1.ThreadID != "t1" || t1.ThreadCount != 2 {
		t.Errorf("t1 row = %s thread %s count %d, want m2 of t1 with count 2", t1.ID, t1.ThreadID, t1.ThreadCount)
	}
	if !t1.IsUnread() {
		t.Error("t1 has an unread message but the row shows read")
	}
	if got[0].IsUnread() {
		t.Error("t2 shows unread")
	}
}

func TestThreads_flaggedIfAnyFlaggedAndNoThreadIsItsOwnRow(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{
		gmailEnv("m1", "t1", 10, `\Seen`, `\Flagged`),
		gmailEnv("m2", "t1", 30, `\Seen`),
		gmailEnv("t1", "", 5, `\Seen`), // no thread; its id must not join thread t1
	}))
	got := s.Threads("acct", "INBOX")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if !got[1].IsFlagged() || got[1].ThreadCount != 2 {
		t.Errorf("t1 row flagged=%v count=%d, want flagged with 2", got[1].IsFlagged(), got[1].ThreadCount)
	}
}

func TestThreadEnvelopes_everyFolderOldestFirstDeduplicated(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("m2", "t1", 30), gmailEnv("x", "t9", 40)}))
	must(t, s.AddMessages("acct", "Sent", []*mail.Envelope{gmailEnv("m1", "t1", 10), gmailEnv("m2", "t1", 30)}))
	var ids []string
	for _, e := range s.ThreadEnvelopes("acct", "t1") {
		ids = append(ids, e.ID)
	}
	if !reflect.DeepEqual(ids, []string{"m1", "m2"}) {
		t.Errorf("thread = %v, want [m1 m2]", ids)
	}
}

func TestThreadOfMessageID(t *testing.T) {
	s := NewMemory()
	reply := gmailEnv("m1", "t1", 10)
	reply.MessageID = "orig@x"
	noThread := gmailEnv("m2", "", 20)
	noThread.MessageID = "lone@x"
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{reply, noThread}))
	if got := s.ThreadOfMessageID("acct", "orig@x"); got != "t1" {
		t.Errorf("thread = %q, want t1", got)
	}
	for _, id := range []string{"lone@x", "unknown@x", ""} {
		if got := s.ThreadOfMessageID("acct", id); got != "" {
			t.Errorf("%q: thread = %q, want none", id, got)
		}
	}
	if got := s.ThreadOfMessageID("other", "orig@x"); got != "" {
		t.Errorf("other account found %q", got)
	}
}

func TestSetFlagsEverywhere_updatesEveryFolder(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.AddMessages("acct", "Work", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.SetFlagsEverywhere("acct", map[string][]string{"a": {`\Seen`}}))
	for _, f := range []string{"INBOX", "Work"} {
		if e, _ := s.Envelope("acct", f, "a"); e == nil || e.IsUnread() {
			t.Errorf("%s: still unread", f)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	s, _ := open(t)
	if got := s.Cursor("acct"); got != "" {
		t.Errorf("fresh cursor = %q", got)
	}
	must(t, s.SetCursor("acct", "12345"))
	must(t, s.SetCursor("acct", "12399"))
	if got := s.Cursor("acct"); got != "12399" {
		t.Errorf("cursor = %q, want 12399", got)
	}
	if got := s.Cursor("other"); got != "" {
		t.Errorf("other account's cursor = %q", got)
	}
	var nilStore *Store
	if nilStore.Cursor("acct") != "" || nilStore.SetCursor("acct", "1") != nil {
		t.Error("nil store must hold no cursor")
	}
}

func TestSharedBodyNewLabelAfterDownload(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.PutBody("acct", "", "a", []byte("raw a")))
	must(t, s.AddMessages("acct", "Work", []*mail.Envelope{gmailEnv("a", "t", 1)})) // labelled later
	missing, err := s.MissingBodies("acct", "Work")
	must(t, err)
	if len(missing) != 0 {
		t.Errorf("missing = %v; the shared body is already downloaded", missing)
	}
}

func TestIMAPSameUIDOtherFolderStillMissing(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("acct", "INBOX", envs(7)))
	must(t, s.PutBody("acct", "INBOX", "7", []byte("raw")))
	must(t, s.AddMessages("acct", "Archive", envs(7))) // another message with the same UID
	missing, err := s.MissingBodies("acct", "Archive")
	must(t, err)
	if !reflect.DeepEqual(missing, []string{"7"}) {
		t.Errorf("missing = %v, want [7]: IMAP UIDs are per folder", missing)
	}
}

func TestResetFolderEmptyNameRefused(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("acct", "INBOX", envs(7)))
	must(t, s.PutBody("acct", "INBOX", "7", []byte("raw")))
	if err := s.ResetFolder("acct", "", 1); err == nil {
		t.Error("ResetFolder with no folder name succeeded")
	}
	if err := s.RemoveMessages("acct", "", []string{"7"}); err == nil {
		t.Error("RemoveMessages with no folder name succeeded")
	}
	must(t, s.SetUIDValidity("acct", "", 1)) // a nameless folder somehow recorded
	must(t, s.PruneFolders("acct", []string{"INBOX"}))
	if _, err := os.Stat(filepath.Join(dir, "acct")); err != nil {
		t.Fatalf("the account directory is gone: %v", err)
	}
	if raw, ok := s.Body("acct", "INBOX", "7"); !ok || string(raw) != "raw" {
		t.Errorf("INBOX/7 body = %q, %v; want it kept", raw, ok)
	}
}

func TestUpsertRejectsEnvelopeWithoutID(t *testing.T) {
	s := NewMemory()
	if err := s.AddMessages("a", "INBOX", []*mail.Envelope{{Subject: "no id"}}); err == nil {
		t.Error("an envelope with neither ID nor UID was stored")
	}
	if err := s.PutEnvelopes("a", "INBOX", []*mail.Envelope{{Subject: "no id"}}); err == nil {
		t.Error("PutEnvelopes stored an envelope with neither ID nor UID")
	}
	if _, ok := s.Envelope("a", "INBOX", "0"); ok {
		t.Error("stored as id 0")
	}
}

// --- attachment sign ---

const plainRaw = "From: a@example.com\r\nSubject: hi\r\n\r\njust text\r\n"

const pdfRaw = "From: a@example.com\r\n" +
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

func signOf(t *testing.T, s *Store, account, folder, id string) bool {
	t.Helper()
	e, ok := s.Envelope(account, folder, id)
	if !ok {
		t.Fatalf("%s/%s/%s not stored", account, folder, id)
	}
	return e.HasAttach
}

func TestPutBody_setsAttachSign(t *testing.T) {
	s, _ := open(t)
	guessNo, guessYes := env(1), env(2)
	guessYes.HasAttach = true
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{guessNo, guessYes}))
	must(t, s.PutBody("a", "INBOX", "1", []byte(pdfRaw)))
	must(t, s.PutBody("a", "INBOX", "2", []byte(plainRaw)))
	if !signOf(t, s, "a", "INBOX", "1") {
		t.Error("a body with a PDF attachment did not set the sign")
	}
	if signOf(t, s, "a", "INBOX", "2") {
		t.Error("a body without files left the server's guess standing")
	}
}

func TestPutEnvelopes_keepsKnownSign(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(1)))
	must(t, s.PutBody("a", "INBOX", "1", []byte(pdfRaw)))
	must(t, s.PutEnvelopes("a", "INBOX", envs(1))) // a later sync: guess false
	if !signOf(t, s, "a", "INBOX", "1") {
		t.Error("a later sync wiped the sign learned from the body")
	}
}

func TestThreads_signIfAnyMember(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("m1", "t1", 10), gmailEnv("m2", "t1", 20)}))
	must(t, s.PutBody("acct", "", "m1", []byte(pdfRaw)))
	must(t, s.PutBody("acct", "", "m2", []byte(plainRaw)))
	rows := s.Threads("acct", "INBOX")
	if len(rows) != 1 || !rows[0].HasAttach {
		t.Errorf("rows = %+v, want one conversation with the sign (its older message has a file)", rows)
	}
}

func TestFillAttachSigns(t *testing.T) {
	s, dir := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(1)))
	must(t, s.AddMessages("g", "INBOX", []*mail.Envelope{gmailEnv("x", "t", 1)}))
	// Bodies written by an older m9s: on disk, has_body set, no sign yet.
	must(t, os.MkdirAll(filepath.Join(dir, "a", "INBOX"), 0o700))
	must(t, os.MkdirAll(filepath.Join(dir, "g"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "a", "INBOX", "1.eml"), []byte(pdfRaw), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "g", "x.eml"), []byte(plainRaw), 0o600))
	_, err := s.db.Exec("UPDATE messages SET has_body = 1, has_attach = NULL")
	must(t, err)

	n, err := s.FillAttachSigns(100)
	must(t, err)
	if n != 2 {
		t.Fatalf("filled %d, want 2", n)
	}
	if !signOf(t, s, "a", "INBOX", "1") || signOf(t, s, "g", "INBOX", "x") {
		t.Error("signs not taken from the stored bodies")
	}
	if n, _ := s.FillAttachSigns(100); n != 0 {
		t.Errorf("second pass filled %d, want 0", n)
	}
}

func TestFillAttachSigns_missingFileIsDownloadedAgain(t *testing.T) {
	s, _ := open(t)
	must(t, s.AddMessages("a", "INBOX", envs(1)))
	_, err := s.db.Exec("UPDATE messages SET has_body = 1, has_attach = NULL")
	must(t, err)
	if n, err := s.FillAttachSigns(100); err != nil || n != 1 {
		t.Fatalf("FillAttachSigns = %d, %v; want the row handled", n, err)
	}
	missing, err := s.MissingBodies("a", "INBOX")
	must(t, err)
	if len(missing) != 1 {
		t.Errorf("missing = %v, want the message whose file is gone", missing)
	}
	if n, _ := s.FillAttachSigns(100); n != 0 {
		t.Errorf("second pass handled %d, want 0", n)
	}
}

func TestKnownAttachSigns_onlyFromBodies(t *testing.T) {
	s, _ := open(t)
	guess := env(2)
	guess.HasAttach = true
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{env(1), guess}))
	must(t, s.PutBody("a", "INBOX", "1", []byte(pdfRaw)))
	got := s.KnownAttachSigns("a", "INBOX")
	if len(got) != 1 || !got["1"] {
		t.Errorf("known = %v, want only 1:true (2 is a server guess)", got)
	}
}

func TestEnvelopeCategoriesRoundTrip(t *testing.T) {
	s, _ := open(t)
	e := env(1)
	e.Categories = []string{"Work", "Травел"}
	must(t, s.PutEnvelopes("acct", "INBOX", []*mail.Envelope{e}))
	got, ok := s.Envelope("acct", "INBOX", "1")
	if !ok || !reflect.DeepEqual(got.Categories, []string{"Work", "Травел"}) {
		t.Fatalf("categories = %v (ok %v)", got.Categories, ok)
	}
	e2 := env(1)
	e2.Categories = []string{"Work"}
	must(t, s.PutEnvelopes("acct", "INBOX", []*mail.Envelope{e2}))
	got, _ = s.Envelope("acct", "INBOX", "1")
	if !reflect.DeepEqual(got.Categories, []string{"Work"}) {
		t.Fatalf("after second put: %v", got.Categories)
	}
}

func TestFolderCursor(t *testing.T) {
	s, _ := open(t)
	if c := s.FolderCursor("acct", "INBOX"); c != "" {
		t.Fatalf("unset = %q", c)
	}
	must(t, s.SetFolderCursor("acct", "INBOX", "d1"))
	if c := s.FolderCursor("acct", "INBOX"); c != "d1" {
		t.Fatalf("set = %q", c)
	}
	if c := s.FolderCursor("acct", "Archive"); c != "" {
		t.Fatalf("other folder = %q", c)
	}
	must(t, s.SetUIDValidity("acct", "INBOX", 3))
	if c := s.FolderCursor("acct", "INBOX"); c != "d1" {
		t.Fatalf("after SetUIDValidity = %q", c)
	}
	must(t, s.SetFolderCursor("acct", "INBOX", "d2"))
	if uv, _, _ := s.FolderState("acct", "INBOX"); uv != 3 {
		t.Fatalf("SetFolderCursor lost uidvalidity: %d", uv)
	}
	must(t, s.ResetFolder("acct", "INBOX", 4))
	if c := s.FolderCursor("acct", "INBOX"); c != "" {
		t.Fatalf("after ResetFolder = %q", c)
	}
	must(t, s.SetFolderCursor("acct", "Gone", "g1"))
	must(t, s.PruneFolders("acct", []string{"INBOX"}))
	if c := s.FolderCursor("acct", "Gone"); c != "" {
		t.Fatalf("pruned folder kept its cursor: %q", c)
	}
}

func TestTokenSaved(t *testing.T) {
	s, _ := open(t)
	if !s.TokenSaved("acct").IsZero() {
		t.Fatalf("never saved = %v", s.TokenSaved("acct"))
	}
	when := time.Date(2026, 11, 23, 14, 5, 9, 700_000_000, time.Local)
	must(t, s.SetTokenSaved("acct", when))
	if got := s.TokenSaved("acct"); !got.Equal(when.Truncate(time.Second)) {
		t.Fatalf("TokenSaved = %v, want %v", got, when.Truncate(time.Second))
	}
	must(t, s.SetCursor("acct", "h1"))
	if got := s.TokenSaved("acct"); !got.Equal(when.Truncate(time.Second)) {
		t.Fatalf("SetCursor lost it: %v", got)
	}
	must(t, s.SetTokenSaved("acct", when.Add(time.Hour)))
	if c := s.Cursor("acct"); c != "h1" {
		t.Fatalf("SetTokenSaved lost the cursor: %q", c)
	}
}

// A message stored under one label is found by id from any folder, so a
// sync does not fetch its headers again for another label.
func TestEnvelopesByID_findsAMessageInAnyFolder(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("acct", "INBOX", []*mail.Envelope{gmailEnv("a", "t", 1)}))
	must(t, s.AddMessages("acct", "Work", []*mail.Envelope{gmailEnv("b", "t", 2)}))
	must(t, s.AddMessages("other", "INBOX", []*mail.Envelope{gmailEnv("c", "t", 3)}))

	got := s.EnvelopesByID("acct", []string{"a", "b", "c", "missing"})
	if len(got) != 2 || got["a"] == nil || got["a"].ID != "a" || got["b"] == nil || got["b"].SortKey != 2 {
		t.Fatalf("got %v, want a and b (c is another account's)", got)
	}
	if n := len(s.EnvelopesByID("acct", nil)); n != 0 {
		t.Errorf("no ids found %d", n)
	}
	var nilStore *Store
	if nilStore.EnvelopesByID("acct", []string{"a"}) != nil {
		t.Error("nil store must hold nothing")
	}
}

// A Gmail delete prunes after every message, so the prune must not grow
// with files × rows: 20,000 of each (a real mailbox) took a minute.
func TestPruneSharedBodies_largeMailboxIsQuick(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 20,000 messages")
	}
	s, dir := open(t)
	const n = 20000
	list := make([]*mail.Envelope, n)
	for i := range list {
		list[i] = gmailEnv(fmt.Sprintf("m%05d", i), "t", int64(i))
	}
	must(t, s.AddMessages("acct", "INBOX", list))
	must(t, os.MkdirAll(filepath.Join(dir, "acct"), 0o700))
	for i := range n + 1 { // one more file than rows: an orphan
		must(t, os.WriteFile(filepath.Join(dir, "acct", fmt.Sprintf("m%05d.eml", i)), []byte("raw"), 0o600))
	}

	start := time.Now()
	must(t, s.PruneSharedBodies("acct"))
	took := time.Since(start)
	if took > 2*time.Second {
		t.Errorf("prune of %d bodies took %v", n, took)
	}
	if _, err := os.Stat(filepath.Join(dir, "acct", fmt.Sprintf("m%05d.eml", n))); !os.IsNotExist(err) {
		t.Errorf("the orphan is still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "acct", "m00000.eml")); err != nil {
		t.Errorf("prune dropped a body still referenced: %v", err)
	}
}

// Gmail looks messages up by id alone (flags everywhere, shared bodies,
// envelopes by id); the key starts (account, folder), so without an index
// each lookup scans the account.
func TestLookupByIDUsesAnIndex(t *testing.T) {
	s, _ := open(t)
	var id, parent, notUsed int
	var detail string
	must(t, s.db.QueryRow("EXPLAIN QUERY PLAN UPDATE messages SET flags = '[]' WHERE account = ? AND id = ?", "a", "1").
		Scan(&id, &parent, &notUsed, &detail))
	if !strings.Contains(detail, "messages_id") {
		t.Errorf("plan = %q, want the messages_id index", detail)
	}
}
