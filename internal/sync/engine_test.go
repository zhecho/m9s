package sync_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	gosync "sync"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
)

// mockClient implements imapadapter.Client for testing.
type mockClient struct {
	passwords   []string // every password Connect was given
	mu          gosync.Mutex
	connectErr  error   // returned by every Connect when set
	connectErrs []error // consumed one per Connect call, before connectErr applies
	closed      chan struct{}
	connects    int
	aborts      int
	hangStatus  bool // Status blocks until the connection is closed (half-open socket)
	hangClose   bool // Close blocks until Abort (IDLE DONE on a half-open socket)
	appends     int
	idleNotify  chan<- imapadapter.IDLENotification
	noUIDPlus   bool // Append returns uid 0, as servers without UIDPLUS do
	appendErr   error
	lastAppend  []byte
	foldersResp []*mail.Folder
	foldersErr  error
	statusResp  map[string]*mail.MailboxStatus
	appendUID   uint32

	storeCalls     int
	fetchBodyCalls int

	// search
	searchResp     []uint32
	searchFolder   string
	searchText     []string
	fetchEnvFolder string
	fetchEnvUIDs   []uint32

	// delete tracking
	moveCalledSrc    string
	moveCalledUID    uint32
	moveCalledDst    string
	moveErr          error
	expungeCalled    bool
	expungeFolder    string
	expungeUID       uint32
	expungeAllFolder string
	expungeErr       error
}

func newMock(folders []*mail.Folder) *mockClient { return &mockClient{foldersResp: folders} }

func (m *mockClient) Connect(_ context.Context, pass string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.passwords = append(m.passwords, pass)
	if len(m.connectErrs) > 0 {
		err := m.connectErrs[0]
		m.connectErrs = m.connectErrs[1:]
		if err != nil {
			return err
		}
	} else if m.connectErr != nil {
		return m.connectErr
	}
	m.closed = make(chan struct{})
	m.connects++
	return nil
}

// Closed returns the channel that ends with the current mock connection.
func (m *mockClient) Closed() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed == nil {
		return nil
	}
	return m.closed
}

// Abort drops the connection without any goodbye, like closing the socket.
func (m *mockClient) Abort() {
	m.mu.Lock()
	m.aborts++
	m.mu.Unlock()
	m.kill()
}

func (m *mockClient) abortCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aborts
}

func (m *mockClient) appendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appends
}

// kill simulates the server dropping the current connection.
func (m *mockClient) kill() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed != nil {
		select {
		case <-m.closed:
		default:
			close(m.closed)
		}
	}
}
func (m *mockClient) Close() error {
	m.mu.Lock()
	hang, closed := m.hangClose, m.closed
	m.mu.Unlock()
	if hang && closed != nil {
		<-closed // only an Abort (or kill) gets us out
	}
	return nil
}
func (m *mockClient) Folders(_ context.Context) ([]*mail.Folder, error) {
	return m.foldersResp, m.foldersErr
}
func (m *mockClient) FetchHeaders(_ context.Context, _ string, _, _ int) ([]*mail.Envelope, error) {
	return nil, nil
}
func (m *mockClient) FetchBody(_ context.Context, _ string, _ uint32) (*mail.Message, error) {
	m.fetchBodyCalls++
	return nil, nil
}
func (m *mockClient) Store(_ context.Context, _ string, _ uint32, _ []string, _ bool) error {
	m.storeCalls++
	return nil
}
func (m *mockClient) Search(_ context.Context, folder string, criteria *imap.SearchCriteria) ([]uint32, error) {
	m.searchFolder, m.searchText = folder, criteria.Text
	return m.searchResp, nil
}
func (m *mockClient) FetchEnvelopes(_ context.Context, folder string, uids []uint32) ([]*mail.Envelope, error) {
	m.fetchEnvFolder, m.fetchEnvUIDs = folder, append([]uint32(nil), uids...)
	sorted := slices.Clone(uids) // a server answers in UID order
	slices.Sort(sorted)
	envs := make([]*mail.Envelope, 0, len(sorted))
	for _, uid := range sorted {
		envs = append(envs, searchEnvelope(uid))
	}
	return envs, nil
}
func (m *mockClient) StartIDLE(_ context.Context, _ string, notify chan<- imapadapter.IDLENotification) error {
	m.mu.Lock()
	m.idleNotify = notify
	m.mu.Unlock()
	return nil
}

// push delivers an IDLE notification as the server would.
func (m *mockClient) push(t *testing.T, n imapadapter.IDLENotification) {
	t.Helper()
	m.mu.Lock()
	notify := m.idleNotify
	m.mu.Unlock()
	if notify == nil {
		t.Fatal("IDLE was not started")
	}
	notify <- n
}
func (m *mockClient) StopIDLE() {}
func (m *mockClient) Status(_ context.Context, mailbox string) (*mail.MailboxStatus, error) {
	m.mu.Lock()
	hang, closed := m.hangStatus, m.closed
	m.mu.Unlock()
	if hang && closed != nil {
		<-closed
		return nil, fmt.Errorf("imap STATUS: connection closed")
	}
	if m.statusResp != nil {
		if s, ok := m.statusResp[mailbox]; ok {
			return s, nil
		}
	}
	return &mail.MailboxStatus{}, nil
}
func (m *mockClient) Append(_ context.Context, _ string, data []byte, _ []imap.Flag) (uint32, error) {
	m.mu.Lock()
	m.appends++
	m.mu.Unlock()
	if m.appendErr != nil {
		return 0, m.appendErr
	}
	m.lastAppend = append([]byte(nil), data...)
	if m.noUIDPlus {
		return 0, nil
	}
	uid := m.appendUID
	if uid == 0 {
		uid = 1
	}
	return uid, nil
}
func (m *mockClient) Move(_ context.Context, srcFolder string, uid uint32, dstFolder string) error {
	m.moveCalledSrc = srcFolder
	m.moveCalledUID = uid
	m.moveCalledDst = dstFolder
	return m.moveErr
}
func (m *mockClient) ExpungeAll(_ context.Context, folder string) (int, error) {
	m.expungeAllFolder = folder
	return 0, nil
}

func (m *mockClient) Expunge(_ context.Context, folder string, uid uint32) error {
	m.expungeCalled = true
	m.expungeFolder, m.expungeUID = folder, uid
	return m.expungeErr
}

func newTestAccount(name string) *config.Account {
	return &config.Account{
		Name: name,
		IMAP: config.IMAPConfig{Host: "localhost", Port: 993, Password: "pass"},
		SMTP: config.SMTPConfig{Host: "localhost", Port: 587},
		Folders: config.FolderConfig{
			BadgePoll:     config.Duration(50 * time.Millisecond),
			BodyCachePoll: config.Duration(10 * time.Minute),
		},
	}
}

func TestEngine_emitsFoldersUpdated(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "Sent", Delimiter: "/"},
		},
	}

	events := make(chan msync.Event, 16)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	eng.Start(ctx)

	fu := nextEvent[msync.FoldersUpdated](t, events)
	if fu.AccountName != "test" {
		t.Errorf("want AccountName=test, got %q", fu.AccountName)
	}
	if len(fu.Folders) == 0 {
		t.Error("want at least one folder")
	}
}

func TestEngine_emitsBadgeUpdated(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
		},
		statusResp: map[string]*mail.MailboxStatus{
			"INBOX": {Messages: 10, Unseen: 3},
		},
	}

	events := make(chan msync.Event, 16)
	acct := newTestAccount("test")
	acct.Folders.BadgePoll = config.Duration(30 * time.Millisecond)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	eng.Start(ctx)

	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if bu, ok := ev.(msync.BadgeUpdated); ok {
				if bu.FolderName == "INBOX" && bu.Unseen == 3 {
					return // test passes
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for BadgeUpdated with Unseen=3")
		}
	}
}

func TestEngine_setDraftStore(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	// Must not panic; just verifies the setter is reachable.
	eng.SetDraftStore(nil)
}

func TestEngine_fetchHeaders(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	envs, err := eng.FetchHeaders(ctx, "INBOX", 0, 20)
	if err != nil {
		t.Fatalf("FetchHeaders: %v", err)
	}
	if envs != nil {
		t.Errorf("mock returns nil, got %v", envs)
	}
}

func TestEngine_fetchBody(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	msg, err := eng.FetchBody(ctx, "INBOX", "1")
	if err != nil {
		t.Fatalf("FetchBody: %v", err)
	}
	if msg != nil {
		t.Errorf("mock returns nil, got %v", msg)
	}
}

func TestEngine_store(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	if err := eng.SetFlags(ctx, "INBOX", "1", []string{`\Seen`}, true); err != nil {
		t.Fatalf("SetFlags: %v", err)
	}
}

func TestEngine_folders(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	folders, err := eng.Folders(ctx)
	if err != nil {
		t.Fatalf("Folders: %v", err)
	}
	if len(folders) == 0 {
		t.Error("expected at least one folder")
	}
}

func TestEngine_appendDraft(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	id, err := eng.SaveDraft(ctx, []byte("EML data"), "")
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if id == "" {
		t.Error("expected an id")
	}
}

func TestEngine_folders_errorReturnsCached(t *testing.T) {
	mc := &mockClient{foldersErr: fmt.Errorf("network error")}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	// No cached folders yet — returns nil slice without error.
	folders, err := eng.Folders(ctx)
	if err != nil {
		t.Fatalf("Folders error path should not propagate error, got: %v", err)
	}
	if folders != nil {
		t.Errorf("want nil cached folders on first error, got %v", folders)
	}
}

func TestEngine_appendDraft_draftsAttribute(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "[Gmail]/Drafts", Delimiter: "/", Attributes: []string{`\Drafts`}},
		},
		appendUID: 42,
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()

	// Populate e.folders by calling Folders first.
	_, _ = eng.Folders(ctx)

	id, err := eng.SaveDraft(ctx, []byte("EML"), "")
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if id != "42" {
		t.Errorf("want id 42, got %q", id)
	}
}

func TestEngine_syncDirtyDrafts(t *testing.T) {
	dir := t.TempDir()
	ds := draft.NewStore(dir)

	// Save a dirty draft.
	id, _ := ds.Save("test", smtpadapter.Draft{
		From: "me@example.com", To: []string{"you@example.com"},
		Subject: "dirty", Body: "hello",
	})

	mc := &mockClient{appendUID: 7}
	events := make(chan msync.Event, 16)
	acct := newTestAccount("test")
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	eng.SetDraftStore(ds)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	eng.Start(ctx)

	// Wait for DraftSynced event.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ds, ok := ev.(msync.DraftSynced); ok {
				if ds.DraftID == id {
					return // success
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for DraftSynced event")
		}
	}
}

func TestEngine_applyFolderConfig_includeExclude(t *testing.T) {
	falseVal := false
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "Spam", Delimiter: "/"},
			{Name: "Archive", Delimiter: "/"},
		},
	}
	events := make(chan msync.Event, 4)
	acct := newTestAccount("test")
	acct.Folders.AutoDetectRaw = &falseVal
	acct.Folders.Include = []config.FolderEntry{{Name: "INBOX"}, {Name: "Archive"}}
	acct.Folders.Exclude = []string{"Archive"}

	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	ctx := context.Background()
	folders, _ := eng.Folders(ctx)

	names := make(map[string]bool)
	for _, f := range folders {
		names[f.Name] = true
	}
	if !names["INBOX"] {
		t.Error("INBOX should be included")
	}
	if names["Spam"] {
		t.Error("Spam should not be included (not in Include list)")
	}
	if names["Archive"] {
		t.Error("Archive should be excluded by Exclude list")
	}
}

func TestEngine_syncAll(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "Sent", Delimiter: "/"},
		},
		statusResp: map[string]*mail.MailboxStatus{
			"INBOX": {Messages: 5, Unseen: 2},
			"Sent":  {Messages: 3, Unseen: 0},
		},
	}

	events := make(chan msync.Event, 16)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	eng.Start(ctx)

	// Drain FoldersUpdated
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal("timeout waiting for FoldersUpdated")
	}

	// Call SyncAll
	eng.SyncAll(ctx)

	// Expect BadgeUpdated for both folders
	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-events:
			if bu, ok := ev.(msync.BadgeUpdated); ok {
				seen[bu.FolderName] = true
			}
		case <-timeout:
			t.Fatalf("timeout: only got BadgeUpdated for %v, want INBOX and Sent", seen)
		}
	}
}

func TestEngine_trashFolderName_byAttribute(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "[Gmail]/Trash", Delimiter: "/", Attributes: []string{`\Trash`}},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	_, _ = eng.Folders(ctx) // populate e.folders
	name := eng.TrashFolderName()
	if name != "[Gmail]/Trash" {
		t.Errorf("want [Gmail]/Trash, got %q", name)
	}
}

func TestEngine_trashFolderName_byHeuristic(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "Deleted Items", Delimiter: "/"},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	_, _ = eng.Folders(ctx)
	name := eng.TrashFolderName()
	if name != "Deleted Items" {
		t.Errorf("want Deleted Items, got %q", name)
	}
}

func TestEngine_trashFolderName_notFound(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	_, _ = eng.Folders(ctx)
	name := eng.TrashFolderName()
	if name != "" {
		t.Errorf("want empty string, got %q", name)
	}
}

func TestEngine_moveToTrash(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "Trash", Delimiter: "/", Attributes: []string{`\Trash`}},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	_, _ = eng.Folders(ctx)

	if err := eng.MoveToTrash(ctx, "INBOX", "42"); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	if mc.moveCalledSrc != "INBOX" {
		t.Errorf("move src: want INBOX, got %q", mc.moveCalledSrc)
	}
	if mc.moveCalledUID != 42 {
		t.Errorf("move uid: want 42, got %d", mc.moveCalledUID)
	}
	if mc.moveCalledDst != "Trash" {
		t.Errorf("move dst: want Trash, got %q", mc.moveCalledDst)
	}
}

func TestEngine_moveToTrash_noTrashFolder(t *testing.T) {
	mc := &mockClient{
		foldersResp: []*mail.Folder{
			{Name: "INBOX", Delimiter: "/"},
		},
	}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()
	_, _ = eng.Folders(ctx)

	err := eng.MoveToTrash(ctx, "INBOX", "1")
	if err == nil {
		t.Fatal("expected error when no trash folder found")
	}
}

func TestEngine_permanentDelete(t *testing.T) {
	mc := &mockClient{}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx := context.Background()

	if err := eng.PermanentDelete(ctx, "Trash", "7"); err != nil {
		t.Fatalf("PermanentDelete: %v", err)
	}
	if !mc.expungeCalled {
		t.Error("expected Expunge to be called on the IMAP client")
	}
}

func TestEngine_deleteDraftCopyExpungesFromDraftsFolder(t *testing.T) {
	mc := &mockClient{foldersResp: []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Entwürfe", Delimiter: "/", Attributes: []string{`\Drafts`}},
	}}
	events := make(chan msync.Event, 4)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	if _, err := eng.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := eng.DeleteDraftCopy(context.Background(), "42"); err != nil {
		t.Fatalf("DeleteDraftCopy: %v", err)
	}
	if mc.expungeFolder != "Entwürfe" || mc.expungeUID != 42 {
		t.Errorf("expunged %q uid %d, want the \\Drafts folder uid 42", mc.expungeFolder, mc.expungeUID)
	}
}

func TestEngine_idleNotificationRefreshesListAndCount(t *testing.T) {
	cmd := twoFolderMock()
	idle := newMock(nil)
	_, events := startSlowPollEngine2(t, cmd, idle)
	nextStatus(t, events, msync.StateConnected)
	awaitBadges(t, events, "INBOX", "Sent") // initial pass done

	idle.push(t, imapadapter.IDLENotification{Folder: "INBOX", Exists: 11})

	nm := nextEvent[msync.NewMail](t, events)
	if nm.AccountName != "test" || nm.FolderName != "INBOX" {
		t.Errorf("NewMail = %+v, want test/INBOX", nm)
	}
	if bu := nextEvent[msync.BadgeUpdated](t, events); bu.FolderName != "INBOX" {
		t.Errorf("BadgeUpdated for %q, want INBOX's count refreshed", bu.FolderName)
	}
}

// moveEngine is an IMAP engine whose server has folders; cfg may change the
// account before the folders are read.
func moveEngine(t *testing.T, folders []*mail.Folder, cfg func(*config.Account)) (*msync.Engine, *mockClient) {
	t.Helper()
	mc := &mockClient{foldersResp: folders}
	acct := newTestAccount("test")
	if cfg != nil {
		cfg(acct)
	}
	for _, f := range folders {
		acct.Folders.Include = append(acct.Folders.Include, config.FolderEntry{Name: f.Name})
	}
	eng := msync.NewEngineWithClients(acct, make(chan msync.Event, 16), mc, mc)
	_, _ = eng.Folders(context.Background())
	return eng, mc
}

func TestEngine_archiveFolder(t *testing.T) {
	inbox := &mail.Folder{Name: "INBOX", Delimiter: "/"}
	tests := []struct {
		name    string
		folders []*mail.Folder
		cfg     func(*config.Account)
		want    string
	}{
		{"special-use wins over the name", []*mail.Folder{inbox, {Name: "Archive", Delimiter: "/"},
			{Name: "Old", Delimiter: "/", Attributes: []string{`\Archive`}}}, nil, "Old"},
		{"by name", []*mail.Folder{inbox, {Name: "archive", Delimiter: "/"}}, nil, "archive"},
		{"config wins", []*mail.Folder{inbox, {Name: "Old", Delimiter: "/", Attributes: []string{`\Archive`}}},
			func(a *config.Account) { a.Folders.Archive = "Kept/2026" }, "Kept/2026"},
		{"none", []*mail.Folder{inbox}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, mc := moveEngine(t, tt.folders, tt.cfg)
			if got := eng.ArchiveFolderName(); got != tt.want {
				t.Fatalf("ArchiveFolderName = %q, want %q", got, tt.want)
			}
			err := eng.Archive(context.Background(), "INBOX", "5")
			if tt.want == "" {
				if !errors.Is(err, msync.ErrNoArchiveFolder) {
					t.Fatalf("Archive err = %v, want ErrNoArchiveFolder", err)
				}
				return
			}
			if err != nil || mc.moveCalledSrc != "INBOX" || mc.moveCalledUID != 5 || mc.moveCalledDst != tt.want {
				t.Fatalf("Archive: err %v, move %q %d -> %q", err, mc.moveCalledSrc, mc.moveCalledUID, mc.moveCalledDst)
			}
		})
	}
}

func TestEngine_archiveInArchiveRefuses(t *testing.T) {
	eng, mc := moveEngine(t, []*mail.Folder{{Name: "INBOX"}, {Name: "Archive"}}, nil)
	if err := eng.Archive(context.Background(), "Archive", "5"); err == nil || mc.moveCalledDst != "" {
		t.Fatalf("err = %v, moved to %q", err, mc.moveCalledDst)
	}
}

func TestEngine_move(t *testing.T) {
	eng, mc := moveEngine(t, []*mail.Folder{{Name: "INBOX"}, {Name: "Work"}}, nil)
	if err := eng.Move(context.Background(), "INBOX", "9", "Work"); err != nil {
		t.Fatal(err)
	}
	if mc.moveCalledSrc != "INBOX" || mc.moveCalledUID != 9 || mc.moveCalledDst != "Work" {
		t.Fatalf("move %q %d -> %q", mc.moveCalledSrc, mc.moveCalledUID, mc.moveCalledDst)
	}
	if err := eng.Move(context.Background(), "Work", "9", "Work"); err == nil {
		t.Error("a move onto the same folder must be refused")
	}
}

func TestEngine_moveTargets(t *testing.T) {
	eng, _ := moveEngine(t, []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Drafts", Delimiter: "/", Attributes: []string{`\Drafts`}},
		{Name: "Projects", Delimiter: "/", Attributes: []string{`\NoSelect`}},
		{Name: "Projects/m9s", Delimiter: "/"},
		{Name: "Trash", Delimiter: "/", Attributes: []string{`\Trash`}},
	}, nil)
	got := slices.Sorted(slices.Values(eng.MoveTargets("INBOX")))
	want := []string{"Projects/m9s", "Trash"}
	if !slices.Equal(got, want) {
		t.Errorf("MoveTargets(INBOX) = %v, want %v (not the folder itself, Drafts or a \\NoSelect folder)", got, want)
	}
}
