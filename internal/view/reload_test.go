package view

import (
	"testing"

	"github.com/zhecho/m9s/internal/mail"
)

type bodyLoad struct {
	id       string
	markSeen bool
}

// recordBodyLoads replaces the background body fetch with a recorder.
func recordBodyLoads(ra *RootApp) *[]bodyLoad {
	var loads []bodyLoad
	ra.bodyLoader = func(_ *mail.Account, _ *mail.Folder, env *mail.Envelope, markSeen bool) {
		loads = append(loads, bodyLoad{env.ID, markSeen})
	}
	return &loads
}

// freshCopies returns new envelope objects with the same UIDs, as a reload does.
func freshCopies() []*mail.Envelope { return peopleEnvelopes() }

func TestReload_withFilterNeverSelectsHiddenMessage(t *testing.T) {
	ra := promptApp(t)
	ra.maillist.SetFilter("carol")
	ra.uiApp.SetFocus(ra.maillist)
	loads := recordBodyLoads(ra)

	ra.showEnvelopes(freshCopies()) // e.g. after a delete or a reconnect

	for _, l := range *loads {
		if l.id != "3" {
			t.Errorf("reload loaded message %s, which the carol filter hides", l.id)
		}
		if l.markSeen {
			t.Errorf("reload marked message %s read; nobody chose it", l.id)
		}
	}
	if ra.maillist.FilterQuery() != "carol" || ra.maillist.VisibleCount() != 1 {
		t.Errorf("filter %q rows %d, want carol kept with 1 row", ra.maillist.FilterQuery(), ra.maillist.VisibleCount())
	}
}

func TestReload_keepsMailListCursor(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.maillist.Select(1, 0) // reading Bob's message (UID 2)
	loads := recordBodyLoads(ra)

	ra.showEnvelopes(freshCopies())

	if ra.selectedEnv == nil || ra.selectedEnv.ID != "2" {
		t.Fatalf("selected %+v after reload, want ID 2 kept", ra.selectedEnv)
	}
	if row, _ := ra.maillist.GetSelection(); row != 1 {
		t.Errorf("cursor on row %d, want 1", row)
	}
	for _, l := range *loads {
		if l.markSeen {
			t.Errorf("reload marked message %s read", l.id)
		}
	}
}

func TestReload_selectedMessageGoneFallsBackToTop(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.maillist.Select(1, 0)
	recordBodyLoads(ra)

	envs := freshCopies()
	ra.showEnvelopes([]*mail.Envelope{envs[0], envs[2]}) // Bob's message was deleted

	if ra.selectedEnv == nil || ra.selectedEnv.ID != "1" {
		t.Errorf("selected %+v, want the top message", ra.selectedEnv)
	}
}

func TestMovingInMailListStillMarksRead(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	loads := recordBodyLoads(ra)

	ra.maillist.Select(2, 0) // the user moves onto Carol's message

	if len(*loads) != 1 || (*loads)[0].id != "3" || !(*loads)[0].markSeen {
		t.Errorf("loads = %+v, want Carol's message loaded and marked read", *loads)
	}
}
