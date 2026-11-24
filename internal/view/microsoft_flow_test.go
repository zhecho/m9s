package view

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/graph"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// microsoftFlow runs the real m9s UI on a simulated screen against the real
// Microsoft engine talking to an in-memory fake Graph.
type microsoftFlow struct {
	*flowHarness
	fake *graphtest.Fake
	eng  *msync.MicrosoftEngine
}

const microsoftFlowUser = "m9s-test@outlook.com"

func newMicrosoftFlow(t *testing.T, seed func(f *graphtest.Fake)) *microsoftFlow {
	t.Helper()
	f := graphtest.New()
	t.Cleanup(f.Close)
	if seed != nil {
		seed(f)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acctCfg := &config.Account{Name: "flow", Provider: config.ProviderMicrosoft, KeePassXC: true,
		Microsoft: config.MicrosoftConfig{User: microsoftFlowUser, ClientID: "cid"},
		Folders: config.FolderConfig{
			BadgePoll:     config.Duration(100 * time.Millisecond),
			BodyCachePoll: config.Duration(100 * time.Millisecond),
		}}
	capture := filepath.Join(t.TempDir(), "capture")
	ds := draft.NewStore(t.TempDir())
	events := make(chan msync.Event, 64)
	api := graph.New(f.Server.Client(), graph.WithBaseURL(f.URL()), graph.WithRateLimit(10000, 10000))
	eng := msync.NewMicrosoftEngine(acctCfg, events, st, ds, api)
	ctx, cancel := context.WithCancel(context.Background())

	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	uiApp := ui.NewApp(skin, km)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(140, 40)
	uiApp.SetScreen(screen)

	cfg := &config.Config{}
	cfg.UI.Editor = "sh " + fakeEditor(t, capture) + " " + capture
	ra := NewRootApp(uiApp, cfg, mail.NewManager([]*mail.Account{{Name: "flow", Config: acctCfg}}),
		map[string]msync.Account{"flow": eng}, ds, events)
	ra.SetStore(st)

	done := make(chan struct{})
	go func() { _ = uiApp.Run(); close(done) }()
	eng.Start(ctx)
	t.Cleanup(func() {
		uiApp.Stop()
		<-done
		cancel()
		<-eng.Done()
		st.Close()
	})
	h := &flowHarness{t: t, ra: ra, screen: screen, drafts: ds, capture: capture}
	return &microsoftFlow{flowHarness: h, fake: f, eng: eng}
}

// inFolder lists the fake's messages in a well-known folder.
func (m *microsoftFlow) inFolder(wk string, ids ...string) []graphtest.Message {
	var out []graphtest.Message
	for _, id := range ids {
		if msg, ok := m.fake.Message(id); ok && msg.FolderID == m.fake.FolderID(wk) {
			out = append(out, msg)
		}
	}
	return out
}

// Through the real Microsoft engine: * flags the selected message on the
// server, a archives it to Outlook's Archive.
func TestMicrosoftFlow_starAndArchive(t *testing.T) {
	var older, newer string
	h := newMicrosoftFlow(t, func(f *graphtest.Fake) {
		older = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "older", Received: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)})
		newer = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "newer", Received: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)})
	})
	h.eventually("both listed", func() bool { return h.ra.maillist.VisibleCount() == 2 && h.ra.selectedEnv != nil })
	h.screen.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	h.eventually("mail list focused", h.ra.mailListFocused)
	var selected string
	h.onUI(func() { selected = h.ra.selectedEnv.ID })
	h.eventually("selection read", func() bool { return selected != "" })

	h.key('*')
	h.eventually("flagged on the server", func() bool { m, _ := h.fake.Message(selected); return m.Flagged })
	h.key('a')
	h.eventually("archived", func() bool { return len(h.inFolder("archive", older, newer)) == 1 })
	if got := h.inFolder("archive", older, newer); got[0].ID != selected || !got[0].Flagged {
		t.Errorf("archived %+v, want the flagged selected message", got[0])
	}
	h.eventually("status", func() bool { return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Archived") })
}

// Graph sends the MIME, not the draft: the uploaded copy in Drafts has to
// go, as on Gmail.
func TestMicrosoftFlow_sendingASyncedDraftDeletesItsServerDraft(t *testing.T) {
	h := newMicrosoftFlow(t, nil)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("draft uploaded", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	local := h.localDrafts()[0]
	if m, ok := h.fake.Message(local.SyncedID); !ok || m.FolderID != h.fake.FolderID("drafts") {
		t.Fatalf("server draft = %+v %v", m, ok)
	}
	d, err := h.drafts.Load("flow", local.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.onUI(func() { h.ra.sendFn(d, local.ID, "flow") })
	h.eventually("sent", func() bool { return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Message sent") })
	if n := len(h.fake.Sent()); n != 1 {
		t.Fatalf("%d messages sent, want 1", n)
	}
	h.eventually("server draft gone", func() bool {
		m, _ := h.fake.Message(local.SyncedID)
		return m.FolderID != h.fake.FolderID("drafts")
	})
}
