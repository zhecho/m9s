package ui

import (
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// selScreen is a 20x4 screen: a left pane (cols 0-4) and a right pane
// (cols 5-19) with a line of text on each row.
func selScreen(t *testing.T) tcell.SimulationScreen {
	t.Helper()
	sc := tcell.NewSimulationScreen("UTF-8")
	if err := sc.Init(); err != nil {
		t.Fatal(err)
	}
	sc.SetSize(20, 4)
	rows := []string{"LEFT hello world", "LEFT second line", "LEFT third", "LEFT"}
	for y, r := range rows {
		for x, c := range r {
			sc.SetContent(x, y, c, nil, tcell.StyleDefault)
		}
	}
	return sc
}

func mouseEv(x, y int, b tcell.ButtonMask) *tcell.EventMouse {
	return tcell.NewEventMouse(x, y, b, tcell.ModNone)
}

func newSel(sc tcell.Screen, copied *string) *Selector {
	s := &Selector{
		Regions: func() []Rect { return []Rect{{0, 0, 5, 4}, {5, 0, 15, 4}} },
		Copy:    func(text string) error { *copied = text; return nil },
	}
	s.screen = sc.Size
	s.highlight(sc) // as after a draw
	return s
}

func TestSelector_dragCopiesTextInsideThePane(t *testing.T) {
	sc := selScreen(t)
	var copied string
	s := newSel(sc, &copied)

	if ev, _ := s.mouse(mouseEv(11, 0, tcell.ButtonPrimary), tview.MouseLeftDown); ev == nil {
		t.Fatal("mouse down must still reach the panes")
	}
	// Dragging to the left pane stays clamped to the right pane.
	if ev, _ := s.mouse(mouseEv(2, 1, tcell.ButtonPrimary), tview.MouseMove); ev != nil {
		t.Fatal("a drag must be consumed")
	}
	s.mouse(mouseEv(10, 1, tcell.ButtonPrimary), tview.MouseMove)
	if ev, _ := s.mouse(mouseEv(10, 1, 0), tview.MouseLeftUp); ev != nil {
		t.Fatal("the release ending a selection must be consumed")
	}
	if want := "world\nsecond"; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}

// One terminal event that both moves and releases: tview fires MouseMove, we
// consume it (nil), and tview then hands that nil event to MouseLeftUp (and
// a click after it).
func TestSelector_releaseInTheSameEventAsTheMove(t *testing.T) {
	sc := selScreen(t)
	var copied string
	s := newSel(sc, &copied)
	s.mouse(mouseEv(11, 0, tcell.ButtonPrimary), tview.MouseLeftDown)
	s.mouse(mouseEv(10, 1, tcell.ButtonPrimary), tview.MouseMove)
	if ev, _ := s.mouse(nil, tview.MouseLeftUp); ev != nil {
		t.Fatal("the release ending a selection must be consumed")
	}
	if ev, _ := s.mouse(nil, tview.MouseLeftClick); ev != nil {
		t.Fatal("a consumed event must stay consumed")
	}
	if want := "world\nsecond"; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}

func TestSelector_highlightsSelection(t *testing.T) {
	sc := selScreen(t)
	var copied string
	s := newSel(sc, &copied)
	s.mouse(mouseEv(5, 0, tcell.ButtonPrimary), tview.MouseLeftDown)
	s.mouse(mouseEv(9, 0, tcell.ButtonPrimary), tview.MouseMove)
	s.highlight(sc)
	if _, _, st, _ := sc.GetContent(7, 0); attrs(st)&tcell.AttrReverse == 0 {
		t.Fatal("selected cell not highlighted")
	}
	if _, _, st, _ := sc.GetContent(12, 0); attrs(st)&tcell.AttrReverse != 0 {
		t.Fatal("cell outside the selection highlighted")
	}
}

func TestSelector_clickIsNotASelection(t *testing.T) {
	sc := selScreen(t)
	var copied string
	s := newSel(sc, &copied)
	s.mouse(mouseEv(6, 0, tcell.ButtonPrimary), tview.MouseLeftDown)
	if ev, _ := s.mouse(mouseEv(6, 0, 0), tview.MouseLeftUp); ev == nil {
		t.Fatal("a plain click must reach the panes")
	}
	if copied != "" || s.shown {
		t.Fatalf("a click selected %q", copied)
	}
}

func TestSelector_borderDragIsNotASelection(t *testing.T) {
	sc := selScreen(t)
	var copied string
	s := newSel(sc, &copied)
	s.Busy = func() bool { return true }
	s.mouse(mouseEv(6, 0, tcell.ButtonPrimary), tview.MouseLeftDown)
	if ev, _ := s.mouse(mouseEv(6, 2, tcell.ButtonPrimary), tview.MouseMove); ev == nil {
		t.Fatal("a border drag must reach the split pane")
	}
	s.mouse(mouseEv(6, 2, 0), tview.MouseLeftUp)
	if copied != "" {
		t.Fatalf("border drag copied %q", copied)
	}
}

func attrs(st tcell.Style) tcell.AttrMask { _, _, a := st.Decompose(); return a }
