package sync_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

func twoFolderMock() *mockClient {
	mc := newMock([]*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/"},
	})
	mc.statusResp = map[string]*mail.MailboxStatus{
		"INBOX": {Messages: 10, Unseen: 3},
		"Sent":  {Messages: 7, Unseen: 0},
	}
	return mc
}

// startSlowPollEngine starts an engine whose badge ticker will not fire during the test.
func startSlowPollEngine(t *testing.T, mc *mockClient) (*msync.Engine, chan msync.Event) {
	t.Helper()
	return startSlowPollEngine2(t, mc, mc)
}

// startSlowPollEngine2 is startSlowPollEngine with separate command and idle connections.
func startSlowPollEngine2(t *testing.T, cmd, idle *mockClient) (*msync.Engine, chan msync.Event) {
	t.Helper()
	acct := newTestAccount("test")
	acct.Folders.BadgePoll = config.Duration(time.Hour)
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, idle, cmd)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)
	return eng, events
}

// awaitBadges drains events until a BadgeUpdated has been seen for each name.
func awaitBadges(t *testing.T, events <-chan msync.Event, names ...string) map[string]msync.BadgeUpdated {
	t.Helper()
	got := map[string]msync.BadgeUpdated{}
	for len(got) < len(names) {
		bu := nextEvent[msync.BadgeUpdated](t, events)
		for _, n := range names {
			if bu.FolderName == n {
				got[n] = bu
			}
		}
	}
	return got
}

func folderNamed(folders []*mail.Folder, name string) *mail.Folder {
	for _, f := range folders {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func TestEngine_initialBadgePass(t *testing.T) {
	_, events := startSlowPollEngine(t, twoFolderMock())
	nextStatus(t, events, msync.StateConnected)

	got := awaitBadges(t, events, "INBOX", "Sent")
	if got["INBOX"].Unseen != 3 || got["INBOX"].Total != 10 {
		t.Errorf("INBOX badge = %+v, want Unseen 3 Total 10", got["INBOX"])
	}
}

func TestEngine_foldersKeepKnownStatus(t *testing.T) {
	eng, events := startSlowPollEngine(t, twoFolderMock())
	awaitBadges(t, events, "INBOX", "Sent")

	folders, err := eng.Folders(context.Background())
	if err != nil {
		t.Fatalf("Folders: %v", err)
	}
	inbox := folderNamed(folders, "INBOX")
	if inbox == nil || inbox.Status == nil {
		t.Fatalf("INBOX or its Status missing: %+v", inbox)
	}
	if inbox.Status.Unseen != 3 || inbox.Status.Messages != 10 {
		t.Errorf("INBOX status = %+v, want Unseen 3 Messages 10", inbox.Status)
	}
}

func TestEngine_foldersUpdatedAfterReconnectCarriesStatus(t *testing.T) {
	mc := twoFolderMock()
	_, events := startSlowPollEngine(t, mc)
	first := nextEvent[msync.FoldersUpdated](t, events)
	if inbox := folderNamed(first.Folders, "INBOX"); inbox == nil || inbox.Status != nil {
		t.Fatalf("first FoldersUpdated: counts are not known yet, got %+v", inbox)
	}
	awaitBadges(t, events, "INBOX", "Sent")

	mc.kill()

	second := nextEvent[msync.FoldersUpdated](t, events)
	inbox := folderNamed(second.Folders, "INBOX")
	if inbox == nil || inbox.Status == nil || inbox.Status.Unseen != 3 {
		t.Errorf("FoldersUpdated after reconnect lost the known counts: %+v", inbox)
	}
}

func TestEngine_refreshBadge(t *testing.T) {
	eng, events := startSlowPollEngine(t, twoFolderMock())
	awaitBadges(t, events, "INBOX", "Sent") // initial pass is over; the ticker is an hour away

	eng.RefreshBadge(context.Background(), "Sent")

	bu := nextEvent[msync.BadgeUpdated](t, events)
	if bu.FolderName != "Sent" || bu.Total != 7 {
		t.Errorf("got %+v, want a Sent badge with Total 7", bu)
	}
}

func TestEngine_handedOutStatusIsACopy(t *testing.T) {
	eng, events := startSlowPollEngine(t, twoFolderMock())
	awaitBadges(t, events, "INBOX", "Sent")

	a, _ := eng.Folders(context.Background())
	folderNamed(a, "INBOX").Status.Unseen = 99 // what the UI goroutine may do

	b, _ := eng.Folders(context.Background())
	if got := folderNamed(b, "INBOX").Status.Unseen; got != 3 {
		t.Errorf("engine's count was changed through a handed-out folder: got %d, want 3", got)
	}
}
