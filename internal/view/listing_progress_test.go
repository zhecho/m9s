package view

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// countingAccount counts the list fetches of each folder.
type countingAccount struct {
	stubAccount
	inbox atomic.Int32
}

func (c *countingAccount) FetchHeaders(ctx context.Context, folder string, page, size int) ([]*mail.Envelope, error) {
	if folder == "INBOX" {
		c.inbox.Add(1)
	}
	return c.stubAccount.FetchHeaders(ctx, folder, page, size)
}

func waitCount(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("INBOX fetched %d times, want %d", n.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func logged() string {
	var msgs []string
	for _, e := range ui.AppLog.Entries() {
		msgs = append(msgs, e.Msg)
	}
	return strings.Join(msgs, "\n")
}

// The open folder reloads while its first listing is stored: the screen
// was empty after the first Gmail sign-in until another folder was opened.
func TestListingProgress_reloadsTheOpenFolder(t *testing.T) {
	ra := newWiredRootApp(t, "a")
	acct := &countingAccount{}
	ra.engines["a"] = acct
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX", "Sent")})
	waitCount(t, &acct.inbox, 1) // opened INBOX: the store was still empty
	before := acct.inbox.Load()

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 100, Total: 150})
	waitCount(t, &acct.inbox, before+1)

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 150, Total: 150, Done: true})
	waitCount(t, &acct.inbox, before+2) // the end always reloads
}

func TestListingProgress_showsInTheHeaderAndLog(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 0, Total: 5400})
	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 1350, Total: 5400})
	if got := infoLine(ra, 4); !strings.Contains(got, "syncing INBOX 1350/5400 (25%)") {
		t.Errorf("status line = %q, want the listing's progress", got)
	}
	if !strings.Contains(logged(), "[a] syncing INBOX: 5400 messages") {
		t.Errorf("log = %q, want the start of the listing", logged())
	}

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 5400, Total: 5400, Done: true})
	if got := infoLine(ra, 4); strings.Contains(got, "syncing") {
		t.Errorf("status line = %q, want no listing once it is done", got)
	}
	if !strings.Contains(logged(), "[a] INBOX synced: 5400 messages") {
		t.Errorf("log = %q, want the end of the listing", logged())
	}
}

func TestListingProgress_ofAnotherAccountStaysOutOfTheHeader(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})
	ra.handleEvent(msync.ListingProgress{AccountName: "b", FolderName: "INBOX", Stored: 1, Total: 10})
	if got := infoLine(ra, 4); strings.Contains(got, "syncing") {
		t.Errorf("status line = %q; that is account b's listing", got)
	}
}

func TestMirrorProgress_showsPercent(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})
	ra.handleEvent(msync.MirrorProgress{AccountName: "a", Messages: 200, Downloaded: 50})
	if got := infoLine(ra, 4); !strings.Contains(got, "saving mail 50/200 (25%)") {
		t.Errorf("status line = %q, want the copy's percent", got)
	}
}

// The header estimates the time left from the rate so far, once a run has
// gone on long enough to tell.
func TestProgress_showsTimeLeft(t *testing.T) {
	ra := twoAccounts(t)
	clock := time.Unix(1_800_000_000, 0)
	ra.now = func() time.Time { return clock }
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 1000, Total: 10000})
	ra.handleEvent(msync.MirrorProgress{AccountName: "a", Messages: 10000, Downloaded: 0})
	clock = clock.Add(5 * time.Second)
	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 1100, Total: 10000})
	if got := infoLine(ra, 4); strings.Contains(got, "left") {
		t.Errorf("status line = %q, too early for an estimate", got)
	}

	// 1000 more in 100s: 8000 to go take 800s.
	clock = clock.Add(95 * time.Second)
	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 2000, Total: 10000})
	// 50 bodies in 100s: 9950 to go take 19900s.
	ra.handleEvent(msync.MirrorProgress{AccountName: "a", Messages: 10000, Downloaded: 50})
	got := infoLine(ra, 4)
	if !strings.Contains(got, "syncing INBOX 2000/10000 (20%) · ~13m left") {
		t.Errorf("status line = %q, want the listing's time left", got)
	}
	if !strings.Contains(got, "saving mail 50/10000 (0%) · ~5h31m left") {
		t.Errorf("status line = %q, want the copy's time left", got)
	}

	ra.handleEvent(msync.ListingProgress{AccountName: "a", FolderName: "INBOX", Stored: 10000, Total: 10000, Done: true})
	if _, ok := ra.started[listingKey(msync.ListingProgress{AccountName: "a", FolderName: "INBOX"})]; ok {
		t.Error("a finished listing is still timed")
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:                "<1m",
		12*time.Minute + 59*time.Second: "12m",
		2*time.Hour + 5*time.Minute:     "2h05m",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
