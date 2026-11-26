package ui_test

import (
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

// mouseRecorder is a pane that records the mouse actions it receives.
type mouseRecorder struct {
	*tview.Box
	got []tview.MouseAction
}

func (m *mouseRecorder) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(a tview.MouseAction, _ *tcell.EventMouse, _ func(tview.Primitive)) (bool, tview.Primitive) {
		m.got = append(m.got, a)
		return true, nil
	}
}

// newSplit lays out a 40×30 split whose top pane shows rows lines.
func newSplit(rows int) (*ui.SplitPane, *mouseRecorder, *mouseRecorder, *[]int) {
	top := &mouseRecorder{Box: tview.NewBox().SetBorder(true)}
	bottom := &mouseRecorder{Box: tview.NewBox().SetBorder(true)}
	var resized []int
	sp := ui.NewSplitPane(top, bottom, rows)
	sp.SetOnResize(func(n int) { resized = append(resized, n) })
	sp.SetRect(0, 0, 40, 30)
	drawPrimitive(sp, 40, 30)
	return sp, top, bottom, &resized
}

func drawPrimitive(p tview.Primitive, w, h int) {
	screen := tcell.NewSimulationScreen("UTF-8")
	_ = screen.Init()
	defer screen.Fini()
	screen.SetSize(w, h)
	p.Draw(screen)
}

func mouse(sp *ui.SplitPane, action tview.MouseAction, y int) (bool, tview.Primitive) {
	ev := tcell.NewEventMouse(5, y, tcell.ButtonNone, tcell.ModNone)
	return sp.MouseHandler()(action, ev, func(tview.Primitive) {})
}

func TestSplitPane_topShowsConfiguredRows(t *testing.T) {
	_, top, bottom, _ := newSplit(10)
	_, ty, _, th := top.GetRect()
	_, by, _, bh := bottom.GetRect()
	if ty != 0 || th != 12 {
		t.Errorf("top at y=%d h=%d, want y=0 h=12 (10 rows + 2 border lines)", ty, th)
	}
	if by != 12 || bh != 18 {
		t.Errorf("bottom at y=%d h=%d, want the rest: y=12 h=18", by, bh)
	}
}

func TestSplitPane_tooTallForTheScreenIsClamped(t *testing.T) {
	top := tview.NewBox()
	bottom := tview.NewBox()
	sp := ui.NewSplitPane(top, bottom, 50)
	sp.SetRect(0, 0, 40, 20)
	drawPrimitive(sp, 40, 20)
	if _, _, _, bh := bottom.GetRect(); bh < 3 {
		t.Errorf("bottom pane squeezed to %d lines; it keeps at least 3", bh)
	}
	if sp.TopRows() != 50 {
		t.Errorf("TopRows = %d; clamping for this screen must not forget the wanted size", sp.TopRows())
	}
}

func TestSplitPane_dragTheBorderDown(t *testing.T) {
	sp, _, _, resized := newSplit(10)

	if ok, capture := mouse(sp, tview.MouseLeftDown, 11); !ok || capture != sp {
		t.Fatalf("pressing on the border must start a drag and capture the mouse (ok=%v capture=%v)", ok, capture)
	}
	mouse(sp, tview.MouseMove, 14)
	mouse(sp, tview.MouseMove, 16)
	mouse(sp, tview.MouseLeftUp, 16)

	if sp.TopRows() != 15 {
		t.Errorf("TopRows after dragging the border from line 11 to 16 = %d, want 15", sp.TopRows())
	}
	if len(*resized) != 1 || (*resized)[0] != 15 {
		t.Errorf("onResize calls = %v, want exactly one, on release, with 15", *resized)
	}
}

func TestSplitPane_dragByThePreviewTopBorderToo(t *testing.T) {
	sp, _, _, _ := newSplit(10)
	mouse(sp, tview.MouseLeftDown, 12) // the bottom pane's top border line
	mouse(sp, tview.MouseMove, 8)
	mouse(sp, tview.MouseLeftUp, 8)
	if sp.TopRows() != 6 {
		t.Errorf("TopRows = %d, want 6", sp.TopRows())
	}
}

func TestSplitPane_dragLimits(t *testing.T) {
	sp, _, _, _ := newSplit(10)
	mouse(sp, tview.MouseLeftDown, 11)
	mouse(sp, tview.MouseMove, 29)
	mouse(sp, tview.MouseLeftUp, 29)
	if sp.TopRows() != 25 {
		t.Errorf("dragged to the bottom: TopRows = %d, want 25 (the preview keeps 3 lines)", sp.TopRows())
	}
	mouse(sp, tview.MouseLeftDown, 26)
	mouse(sp, tview.MouseMove, 0)
	mouse(sp, tview.MouseLeftUp, 0)
	if sp.TopRows() != 1 {
		t.Errorf("dragged to the top: TopRows = %d, want 1 (the list keeps one row)", sp.TopRows())
	}
}

func TestSplitPane_clicksElsewhereReachThePanes(t *testing.T) {
	sp, top, bottom, resized := newSplit(10)
	mouse(sp, tview.MouseLeftClick, 5)
	mouse(sp, tview.MouseLeftClick, 20)
	if len(top.got) != 1 || len(bottom.got) != 1 {
		t.Errorf("clicks reached top %v and bottom %v, want one each", top.got, bottom.got)
	}
	if len(*resized) != 0 || sp.TopRows() != 10 {
		t.Error("a click inside a pane must not resize anything")
	}
}

// keyRecorder is a focusable pane that records the keys it receives.
type keyRecorder struct {
	*tview.Box
	keys []rune
}

func (k *keyRecorder) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(ev *tcell.EventKey, _ func(tview.Primitive)) { k.keys = append(k.keys, ev.Rune()) }
}

func TestSplitPane_keysReachTheFocusedPane(t *testing.T) {
	top := &keyRecorder{Box: tview.NewBox()}
	bottom := &keyRecorder{Box: tview.NewBox()}
	sp := ui.NewSplitPane(top, bottom, 10)
	press := func(r rune) {
		sp.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	}

	top.Focus(nil)
	press('j')
	top.Blur()
	bottom.Focus(nil)
	press('k')

	if string(top.keys) != "j" || string(bottom.keys) != "k" {
		t.Errorf("top got %q, bottom got %q; each key must reach the focused pane", string(top.keys), string(bottom.keys))
	}
}

// newBottomSplit lays out a 40×h split whose bottom pane shows rows lines.
func newBottomSplit(rows, h int) (*ui.SplitPane, *mouseRecorder, *mouseRecorder, *[]int) {
	top := &mouseRecorder{Box: tview.NewBox().SetBorder(true)}
	bottom := &mouseRecorder{Box: tview.NewBox().SetBorder(true)}
	var resized []int
	sp := ui.NewSplitPaneBottom(top, bottom, rows)
	sp.SetOnResize(func(n int) { resized = append(resized, n) })
	sp.SetRect(0, 0, 40, h)
	drawPrimitive(sp, 40, h)
	return sp, top, bottom, &resized
}

// The log pane: the bottom shows its rows, the top gets the rest.
func TestSplitPaneBottom_sizesTheBottom(t *testing.T) {
	_, top, bottom, _ := newBottomSplit(12, 40)
	if _, _, _, bh := bottom.GetRect(); bh != 14 {
		t.Errorf("bottom h=%d, want 14 (12 rows + 2 border lines)", bh)
	}
	if _, _, _, th := top.GetRect(); th != 26 {
		t.Errorf("top h=%d, want 26", th)
	}
}

// On a short terminal the bottom gives way: the top keeps 10 lines.
func TestSplitPaneBottom_topKeepsTen(t *testing.T) {
	_, top, bottom, _ := newBottomSplit(12, 20)
	_, _, _, th := top.GetRect()
	_, _, _, bh := bottom.GetRect()
	if th != 10 || bh != 10 {
		t.Errorf("top h=%d bottom h=%d, want 10 and 10", th, bh)
	}
}

// Dragging the border up makes the bottom taller, and reports its rows.
func TestSplitPaneBottom_dragReportsBottomRows(t *testing.T) {
	sp, _, _, resized := newBottomSplit(12, 40)
	border := 26 // the bottom pane's top line
	mouse(sp, tview.MouseLeftDown, border)
	mouse(sp, tview.MouseMove, border-3)
	mouse(sp, tview.MouseLeftUp, border-3)
	if len(*resized) != 1 || (*resized)[0] != 15 || sp.BottomRows() != 15 {
		t.Errorf("resized %v, BottomRows %d; want 15", *resized, sp.BottomRows())
	}
}

// On a very short space the log still gives way first: it keeps 3 lines
// (border and one line) and the top gets the rest; no size goes negative.
func TestSplitPaneBottom_tinySpace(t *testing.T) {
	_, top, bottom, _ := newBottomSplit(12, 11)
	_, _, _, th := top.GetRect()
	_, _, _, bh := bottom.GetRect()
	if th != 8 || bh != 3 {
		t.Errorf("11 lines: top %d bottom %d, want 8 and 3", th, bh)
	}
	_, top, bottom, _ = newBottomSplit(12, 2)
	_, _, _, th = top.GetRect()
	_, _, _, bh = bottom.GetRect()
	if th < 0 || bh < 0 || th+bh != 2 {
		t.Errorf("2 lines: top %d bottom %d", th, bh)
	}
}

// A click on the border that does not move changes nothing and saves
// nothing.
func TestSplitPane_clickOnBorderSavesNothing(t *testing.T) {
	sp, _, _, resized := newBottomSplit(12, 40)
	mouse(sp, tview.MouseLeftDown, 26)
	mouse(sp, tview.MouseLeftUp, 26)
	if len(*resized) != 0 || sp.BottomRows() != 12 {
		t.Errorf("resized %v, rows %d; want nothing", *resized, sp.BottomRows())
	}
}
