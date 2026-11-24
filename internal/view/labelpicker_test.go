package view

import (
	"context"
	"slices"
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

func pickerKey(p *LabelPicker, k tcell.Key, r rune) {
	p.InputHandler()(tcell.NewEventKey(k, r, tcell.ModNone), func(tview.Primitive) {})
}

func TestLabelPickerAppliesDiff(t *testing.T) {
	var add, remove []string
	applied := false
	p := NewLabelPicker(testSkin(), []string{"Home", "Work"}, []string{"Work"},
		func(a, r []string) { applied, add, remove = true, a, r }, func() {})
	pickerKey(p, tcell.KeyRune, ' ') // Home on
	pickerKey(p, tcell.KeyDown, 0)
	pickerKey(p, tcell.KeyRune, ' ') // Work off
	pickerKey(p, tcell.KeyEnter, 0)
	if !applied || !slices.Equal(add, []string{"Home"}) || !slices.Equal(remove, []string{"Work"}) {
		t.Fatalf("applied=%v add=%v remove=%v", applied, add, remove)
	}
}

func TestLabelPickerEscCancels(t *testing.T) {
	cancelled, applied := false, false
	p := NewLabelPicker(testSkin(), []string{"Work"}, nil,
		func(_, _ []string) { applied = true }, func() { cancelled = true })
	pickerKey(p, tcell.KeyRune, ' ')
	pickerKey(p, tcell.KeyEscape, 0)
	if !cancelled || applied {
		t.Fatalf("cancelled=%v applied=%v", cancelled, applied)
	}
}

func TestLabelPickerUntouchedAppliesNothing(t *testing.T) {
	var add, remove []string
	p := NewLabelPicker(testSkin(), []string{"Work"}, []string{"Work"},
		func(a, r []string) { add, remove = a, r }, func() {})
	pickerKey(p, tcell.KeyEnter, 0)
	if len(add)+len(remove) != 0 {
		t.Fatalf("add=%v remove=%v", add, remove)
	}
}

func TestLabelsKeyOnIMAPShowsHint(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	stub := &stubAccount{rows: peopleEnvelopes()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	ra.maillist.Select(0, 0)
	if ev := sendRune(ra, 'L'); ev != nil {
		t.Fatal("L not consumed")
	}
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "labels: not available for this account") {
		t.Errorf("status = %q", got)
	}
	if ra.uiApp.Pages.HasPage("label-picker") {
		t.Error("picker opened on IMAP")
	}
}

// labelStub is a Gmail-like account with labels.
type labelStub struct {
	stubThreads
	labels  []*mail.Folder
	current []string
}

func (s *labelStub) AccountLabels() []*mail.Folder { return s.labels }
func (s *labelStub) CurrentLabels(threadID, id string) []string {
	s.record("CurrentLabels " + threadID + " " + id)
	return s.current
}
func (s *labelStub) ModifyLabels(_ context.Context, folder, id string, add, remove []string) error {
	s.record("ModifyLabels " + folder + " " + id + " +" + strings.Join(add, ",") + " -" + strings.Join(remove, ","))
	return nil
}

func labelApp(t *testing.T) (*RootApp, *labelStub) {
	t.Helper()
	ra := newWiredRootApp(t, "test")
	stub := &labelStub{current: []string{"Work"}, labels: []*mail.Folder{
		{Name: "INBOX"}, {Name: "Starred", Attributes: []string{`\Flagged`}},
		{Name: "Sent", Attributes: []string{`\Sent`}}, {Name: "Home"}, {Name: "Work"}}}
	stub.thread = convMessages()
	stub.rows = []*mail.Envelope{convRow()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent", "Trash")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	ra.maillist.Select(0, 0)
	return ra, stub
}

func TestLabelsKeyOnConversationAppliesToThread(t *testing.T) {
	ra, stub := labelApp(t)
	before := ra.uiApp.GetFocus()
	if ev := sendRune(ra, 'L'); ev != nil {
		t.Fatal("L not consumed")
	}
	if !ra.uiApp.Pages.HasPage("label-picker") {
		t.Fatal("picker not open")
	}
	p := ra.labelPicker
	if got := p.Names(); !slices.Equal(got, []string{"Home", "Work"}) {
		t.Fatalf("listed %v: system labels must be left out", got)
	}
	stub.waitCall(t, "CurrentLabels T "+ra.selectedEnv.ID)
	pickerKey(p, tcell.KeyRune, ' ') // Home on
	pickerKey(p, tcell.KeyDown, 0)
	pickerKey(p, tcell.KeyRune, ' ') // Work off
	pickerKey(p, tcell.KeyEnter, 0)
	stub.waitCall(t, "ModifyThread T +Home -Work")
	if ra.uiApp.Pages.HasPage("label-picker") {
		t.Error("picker still open")
	}
	if ra.uiApp.GetFocus() != before {
		t.Error("focus not restored")
	}
}

func TestLabelsKeyOnMessageRowUsesModifyLabels(t *testing.T) {
	ra, stub := labelApp(t)
	// A Trash-like list shows single messages: no conversation row.
	ra.currentFolder = &mail.Folder{Name: "Trash"}
	sendRune(ra, 'L')
	p := ra.labelPicker
	pickerKey(p, tcell.KeyRune, ' ')
	pickerKey(p, tcell.KeyEnter, 0)
	stub.waitCall(t, "ModifyLabels Trash "+ra.selectedEnv.ID+" +Home -")
}

func TestLabelsPickerEscRestoresFocus(t *testing.T) {
	ra, _ := labelApp(t)
	before := ra.uiApp.GetFocus()
	sendRune(ra, 'L')
	pickerKey(ra.labelPicker, tcell.KeyEscape, 0)
	if ra.uiApp.Pages.HasPage("label-picker") || ra.uiApp.GetFocus() != before {
		t.Error("Esc did not close the picker and restore focus")
	}
}
