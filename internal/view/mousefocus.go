package view

import (
	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

type mouseHandler = func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive)

// focusPane makes a click focus (and capture the mouse for) pane instead of
// the tview widget it embeds. tview's own handler names the embedded widget,
// which would skip pane's Focus, Blur and InputHandler and leave GetFocus
// matching no pane: no highlighted title, and Tab starting over at Folders.
func focusPane(pane, inner tview.Primitive, h mouseHandler) mouseHandler {
	if h == nil {
		return nil
	}
	swap := func(p tview.Primitive) tview.Primitive {
		if p == inner {
			return pane
		}
		return p
	}
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		consumed, capture := h(action, event, func(p tview.Primitive) { setFocus(swap(p)) })
		if capture == nil {
			return consumed, nil
		}
		return consumed, swap(capture)
	}
}

// MouseHandler focuses the folder pane itself on a click.
func (fv *FolderView) MouseHandler() mouseHandler {
	return focusPane(fv, fv.TreeView, fv.TreeView.MouseHandler())
}

// MouseHandler focuses the mail list pane itself on a click; a click with a
// modifier marks the row instead (see markClick).
func (mlv *MailListView) MouseHandler() mouseHandler {
	return focusPane(mlv, mlv.Table, mlv.markClick(mlv.Table.MouseHandler()))
}

// MouseHandler focuses the preview pane itself on a click.
func (pv *PreviewView) MouseHandler() mouseHandler {
	return focusPane(pv, pv.TextView, pv.TextView.MouseHandler())
}
