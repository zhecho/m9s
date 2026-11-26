package ui

import (
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	splitBorders   = 2 // a bordered pane spends a line above and below its content
	splitMinBottom = 3 // the bottom pane never shrinks below a border and one line
	splitMinTopRow = 1
	// splitMinTopArea is what the top keeps when the bottom pane is the
	// sized one (the log pane): the mail panes above stay usable.
	splitMinTopArea = 10
)

// SplitPane stacks two bordered panes. The top one shows TopRows lines of
// content; the bottom one gets the rest. Dragging the border between them
// with the mouse moves it.
type SplitPane struct {
	*tview.Box
	top, bottom tview.Primitive
	topRows     int
	// sizeBottom: the bottom pane is the one with a size (bottomRows lines),
	// the top gets the rest — the log pane under the mail area.
	sizeBottom bool
	bottomRows int
	dragging   bool
	moved      bool // the pointer moved since the border was grabbed
	grabOffset int  // 0: grabbed the top pane's bottom border, 1: the bottom pane's top border
	onResize   func(rows int)
}

// NewSplitPane creates a split whose top pane shows topRows lines.
func NewSplitPane(top, bottom tview.Primitive, topRows int) *SplitPane {
	return &SplitPane{Box: tview.NewBox(), top: top, bottom: bottom, topRows: max(topRows, splitMinTopRow)}
}

// NewSplitPaneBottom creates a split whose bottom pane shows bottomRows
// lines and whose top pane gets the rest, keeping at least 10 lines.
func NewSplitPaneBottom(top, bottom tview.Primitive, bottomRows int) *SplitPane {
	return &SplitPane{Box: tview.NewBox(), top: top, bottom: bottom, topRows: splitMinTopRow,
		sizeBottom: true, bottomRows: max(bottomRows, splitMinTopRow)}
}

// BottomRows returns how many content lines a bottom-sized split's bottom
// pane is meant to show.
func (s *SplitPane) BottomRows() int { return s.bottomRows }

// SetBottomRows changes how many content lines the bottom pane shows.
func (s *SplitPane) SetBottomRows(rows int) { s.bottomRows = max(rows, splitMinTopRow) }

// Dragging reports whether the border is being dragged right now.
func (s *SplitPane) Dragging() bool { return s.dragging }

// SetOnResize registers a callback fired once a drag ends, with the new rows.
func (s *SplitPane) SetOnResize(fn func(rows int)) { s.onResize = fn }

// TopRows returns how many content lines the top pane is meant to show.
func (s *SplitPane) TopRows() int { return s.topRows }

// SetTopRows changes how many content lines the top pane shows.
func (s *SplitPane) SetTopRows(rows int) { s.topRows = max(rows, splitMinTopRow) }

// topHeight is the top pane's height for a total height h: the wanted size,
// limited so the bottom pane keeps its minimum. The wanted size itself is
// kept, so a larger window gets it back.
func (s *SplitPane) topHeight(h int) int {
	h = max(h, 0)
	if s.sizeBottom {
		if h < splitMinTopArea+splitMinBottom {
			return max(h-splitMinBottom, 0) // the log still gives way first
		}
		return h - max(min(s.bottomRows+splitBorders, h-splitMinTopArea), splitMinBottom)
	}
	want := s.topRows + splitBorders
	limit := h - splitMinBottom
	if limit < splitBorders+splitMinTopRow {
		return h / 2 // tiny window: share it
	}
	return min(want, limit)
}

// Draw lays out and draws both panes.
func (s *SplitPane) Draw(screen tcell.Screen) {
	s.Box.DrawForSubclass(screen, s)
	x, y, w, h := s.GetRect()
	topH := s.topHeight(h)
	s.top.SetRect(x, y, w, topH)
	s.bottom.SetRect(x, y+topH, w, h-topH)
	s.top.Draw(screen)
	s.bottom.Draw(screen)
}

// Focus hands focus to the top pane, like a Flex whose first item has focus.
func (s *SplitPane) Focus(delegate func(p tview.Primitive)) { delegate(s.top) }

// HasFocus reports whether either pane has focus.
func (s *SplitPane) HasFocus() bool { return s.top.HasFocus() || s.bottom.HasFocus() }

// InputHandler passes keys to the pane that has focus. tview delivers every
// key to the root, and each container must hand it down, as Flex does.
func (s *SplitPane) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return s.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		for _, p := range []tview.Primitive{s.top, s.bottom} {
			if p.HasFocus() {
				if handler := p.InputHandler(); handler != nil {
					handler(event, setFocus)
				}
				return
			}
		}
	})
}

// MouseHandler drags the border between the panes and passes every other
// mouse event to the pane under the pointer.
func (s *SplitPane) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return s.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		x, y := event.Position()
		rx, ry, w, h := s.GetRect()
		border := ry + s.topHeight(h) // first line of the bottom pane

		if s.dragging {
			switch action {
			case tview.MouseMove:
				s.moved = true
				s.dragTo(y-ry-s.grabOffset, h)
				return true, s
			case tview.MouseLeftUp:
				s.dragging = false
				if !s.moved { // a click on the border: nothing changes, nothing is saved
					return true, nil
				}
				s.dragTo(y-ry-s.grabOffset, h)
				if s.onResize != nil {
					if s.sizeBottom {
						s.onResize(s.bottomRows)
					} else {
						s.onResize(s.topRows)
					}
				}
				return true, nil
			}
			return true, s
		}

		onBorder := x >= rx && x < rx+w && (y == border-1 || y == border)
		if action == tview.MouseLeftDown && onBorder {
			s.dragging, s.moved = true, false
			s.grabOffset = y - (border - 1)
			return true, s
		}

		for _, p := range []tview.Primitive{s.top, s.bottom} {
			if handler := p.MouseHandler(); handler != nil {
				if px, py, pw, ph := p.GetRect(); x >= px && x < px+pw && y >= py && y < py+ph {
					return handler(action, event, setFocus)
				}
			}
		}
		return false, nil
	})
}

// dragTo puts the border at offset line within a pane of height h: the top
// pane's bottom border follows the pointer.
func (s *SplitPane) dragTo(line, h int) {
	if s.sizeBottom {
		topH := min(max(line+1, splitMinTopArea), h-splitMinBottom)
		s.bottomRows = max(h-topH-splitBorders, splitMinTopRow)
		return
	}
	topH := line + 1
	topH = max(topH, splitBorders+splitMinTopRow)
	topH = min(topH, h-splitMinBottom)
	s.topRows = max(topH-splitBorders, splitMinTopRow)
}
