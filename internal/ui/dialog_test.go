package ui_test

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

type dialogRec struct {
	index int
	label string
	calls int
}

func newTestDialog(focus int) (*ui.Dialog, *dialogRec, *ui.Skin) {
	skin, _ := ui.LoadSkin("")
	rec := &dialogRec{index: -2}
	d := ui.NewDialog(skin, "Permanently delete \"x\"?\nThis cannot be undone.",
		[]ui.DialogButton{{Label: "Delete Forever"}, {Label: "Delete All Forever", Danger: true}, {Label: "Cancel"}},
		focus, func(i int, l string) { rec.index, rec.label = i, l; rec.calls++ })
	return d, rec, skin
}

// cellBackground returns the background colour where label is drawn.
func cellBackground(t *testing.T, d *ui.Dialog, label string) tcell.Color {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 20)
	d.SetRect(0, 0, 100, 20)
	d.Draw(screen)
	screen.Show()
	cells, w, h := screen.GetContents()
	for y := 0; y < h; y++ {
		var row strings.Builder
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				row.WriteRune(r[0])
			} else {
				row.WriteRune(' ')
			}
		}
		if x := strings.Index(row.String(), label); x >= 0 {
			_, bg, _ := cells[y*w+x].Style.Decompose()
			return bg
		}
	}
	t.Fatalf("label %q not drawn", label)
	return tcell.ColorDefault
}

func TestDialog_dangerButtonHasItsOwnColour(t *testing.T) {
	d, _, skin := newTestDialog(2) // focus on Cancel, so neither button below is highlighted
	danger := cellBackground(t, d, "Delete All Forever")
	normal := cellBackground(t, d, "Delete Forever ") // the space keeps it apart from "Delete All Forever"
	if danger != skin.Color("flash_err") {
		t.Errorf("danger button background = %v, want the skin's error colour %v", danger, skin.Color("flash_err"))
	}
	if danger == normal {
		t.Error("the danger button must look different from the others")
	}
}

// focusTracker hands focus down the way tview.Application does and sends
// keys to whatever ends up focused.
type focusTracker struct{ focused tview.Primitive }

func (f *focusTracker) set(p tview.Primitive) {
	f.focused = p
	p.Focus(f.set)
}

func (f *focusTracker) key(k tcell.Key) {
	f.focused.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), f.set)
}

func TestDialog_enterPressesTheFocusedButton(t *testing.T) {
	d, rec, _ := newTestDialog(2)
	var ft focusTracker
	ft.set(d)
	ft.key(tcell.KeyEnter)
	if rec.calls != 1 || rec.label != "Cancel" || rec.index != 2 {
		t.Errorf("done(%d, %q) after Enter, want (2, Cancel): the initial focus decides", rec.index, rec.label)
	}
}

func TestDialog_escapeCancels(t *testing.T) {
	d, rec, _ := newTestDialog(0)
	var ft focusTracker
	ft.set(d)
	ft.key(tcell.KeyEscape)
	if rec.calls != 1 || rec.label != "Cancel" {
		t.Errorf("done(%d, %q) after Esc, want Cancel", rec.index, rec.label)
	}
}

func TestDialog_textAndButtons(t *testing.T) {
	d, _, _ := newTestDialog(0)
	if got := strings.Join(d.ButtonLabels(), ","); got != "Delete Forever,Delete All Forever,Cancel" {
		t.Errorf("ButtonLabels = %q", got)
	}
	if !strings.Contains(d.Text(), "cannot be undone") {
		t.Errorf("Text = %q", d.Text())
	}
}

func TestDialog_tabMovesBetweenButtons(t *testing.T) {
	d, rec, _ := newTestDialog(0)
	var ft focusTracker
	ft.set(d)
	ft.key(tcell.KeyTab)
	ft.key(tcell.KeyEnter)
	if rec.label != "Delete All Forever" {
		t.Errorf("Tab then Enter chose %q, want the second button", rec.label)
	}
}
