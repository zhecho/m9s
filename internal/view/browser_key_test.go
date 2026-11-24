package view

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// browserApp is a wired app showing three messages, whose browser is a
// recorder: opened collects the pages handed to it.
func browserApp(t *testing.T) (ra *RootApp, opened *[]string) {
	t.Helper()
	ra = promptApp(t)
	recordBodyLoads(ra)
	ra.browser.base = t.TempDir()
	t.Cleanup(ra.browser.cleanup)
	opened = new([]string)
	ra.openBrowser = func(path string) error {
		*opened = append(*opened, path)
		return nil
	}
	ra.maillist.Select(0, 0)
	return ra, opened
}

// bodyOf is the downloaded message for the selected row.
func bodyOf(ra *RootApp, text string) *mail.Message {
	return &mail.Message{Envelope: ra.selectedEnv, TextBody: text}
}

func readPage(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBrowserKey_opensTheSelectedMessage(t *testing.T) {
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.currentMsg = bodyOf(ra, "lunch at noon?")

	if ev := sendRune(ra, 'b'); ev != nil {
		t.Error("b must be consumed in the mail list")
	}
	if len(*opened) != 1 {
		t.Fatalf("browser opened %d pages, want 1", len(*opened))
	}
	page := readPage(t, (*opened)[0])
	if !strings.Contains(page, "lunch at noon?") || !strings.Contains(page, "Lunch") {
		t.Errorf("page is not the selected message:\n%s", page)
	}
}

func TestBrowserKey_worksFromThePreview(t *testing.T) {
	ra, opened := browserApp(t)
	ra.currentMsg = bodyOf(ra, "hello")
	ra.uiApp.SetFocus(ra.preview)
	sendRune(ra, 'b')
	if len(*opened) != 1 {
		t.Errorf("browser opened %d pages from the preview, want 1", len(*opened))
	}
}

func TestBrowserKey_doesNothingInTheFolderPane(t *testing.T) {
	ra, opened := browserApp(t)
	ra.currentMsg = bodyOf(ra, "hello")
	ra.uiApp.SetFocus(ra.folders)
	sendRune(ra, 'b')
	if len(*opened) != 0 {
		t.Errorf("b in the folder pane opened %d pages", len(*opened))
	}
}

func TestBrowserKey_nothingSelected(t *testing.T) {
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.maillist.SetFilter("no such sender") // empty list
	sendRune(ra, 'b')
	if len(*opened) != 0 {
		t.Errorf("opened %d pages with no message selected", len(*opened))
	}
}

func TestBrowserKey_waitsForABodyStillDownloading(t *testing.T) {
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	loads := recordBodyLoads(ra)
	env := ra.selectedEnv // body not here yet: currentMsg is nil

	sendRune(ra, 'b')
	if len(*opened) != 0 {
		t.Fatal("opened a page before the body arrived")
	}
	if len(*loads) != 1 || (*loads)[0].id != env.ID || (*loads)[0].markSeen {
		t.Errorf("loads = %+v, want the selected body fetched once, not marked read by this", *loads)
	}

	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env, &mail.Message{Envelope: env, TextBody: "late body"})
	if len(*opened) != 1 || !strings.Contains(readPage(t, (*opened)[0]), "late body") {
		t.Fatalf("opened = %v, want the page once its body arrived", *opened)
	}

	// A later refresh of the same body must not open another tab.
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env, &mail.Message{Envelope: env, TextBody: "late body"})
	if len(*opened) != 1 {
		t.Errorf("opened %d pages, want still 1", len(*opened))
	}
}

func TestBrowserKey_movingAwayCancelsTheWait(t *testing.T) {
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	first := ra.selectedEnv
	sendRune(ra, 'b')
	ra.maillist.Select(1, 0) // the user moved on

	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, first, &mail.Message{Envelope: first, TextBody: "x"})
	second := ra.selectedEnv
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, second, &mail.Message{Envelope: second, TextBody: "y"})
	if len(*opened) != 0 {
		t.Errorf("opened %d pages for a message the user did not ask for", len(*opened))
	}
}

func TestBrowserKey_failureIsReported(t *testing.T) {
	ra, _ := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.currentMsg = bodyOf(ra, "hello")
	ra.openBrowser = func(string) error { return errors.New("no browser here") }
	sendRune(ra, 'b')
	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "no browser here") {
		t.Errorf("flash = %q, want the reason", flash)
	}
}

func TestStop_removesBrowserPages(t *testing.T) {
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.currentMsg = bodyOf(ra, "hello")
	sendRune(ra, 'b')
	ra.Stop()
	if _, err := os.Stat((*opened)[0]); !os.IsNotExist(err) {
		t.Errorf("the page outlived m9s: %v", err)
	}
}

func TestHints_browserAndJumpKeysAreListed(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if !hasHint(generalHints(km), "g/G", "Top/Bottom") {
		t.Errorf("general hints lack g/G: %+v", generalHints(km))
	}
	for _, pane := range []paneKind{paneMailList, panePreview} {
		for _, canEdit := range []bool{false, true} {
			hints := paneHints(km, pane, canEdit)
			if !hasHint(hints, "b", "Browser") {
				t.Errorf("pane %d hints lack b/Browser: %+v", pane, hints)
			}
			if len(hints) > ui.HeaderHeight {
				t.Errorf("pane %d has %d hints; the header shows only %d", pane, len(hints), ui.HeaderHeight)
			}
		}
	}
	if hasHint(paneHints(km, paneFolders, true), "b", "Browser") {
		t.Error("the folder pane must not advertise b")
	}
}

func TestBodyArrived_correctsSign(t *testing.T) {
	ra, _ := browserApp(t)
	env := ra.selectedEnv
	row, _ := ra.maillist.GetSelection()
	withFile := &mail.Message{Envelope: env, Attachments: []*mail.Attachment{{Filename: "a.pdf"}}}
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env, withFile)
	if got := ra.maillist.GetCell(row, 2).Text; got != attachSign {
		t.Errorf("sign after a body with a file = %q, want %q", got, attachSign)
	}
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env, &mail.Message{Envelope: env, TextBody: "x"})
	if got := ra.maillist.GetCell(row, 2).Text; got != "" {
		t.Errorf("sign after a body without files = %q, want none", got)
	}
}
