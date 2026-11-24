package ui

import (
	"github.com/derailed/tview"
)

// App wraps tview.Application and owns the root Pages stack.
type App struct {
	*tview.Application
	Pages     *tview.Pages
	Skin      *Skin
	KeyMap    KeyMap
	StatusBar *StatusBar
	Header    *Header
}

// NewApp creates and wires the application shell.
func NewApp(skin *Skin, km KeyMap) *App {
	tapp := tview.NewApplication()
	pages := tview.NewPages()
	statusBar := NewStatusBar(skin, tapp)
	header := NewHeader(skin)
	return &App{
		Application: tapp,
		Pages:       pages,
		Skin:        skin,
		KeyMap:      km,
		StatusBar:   statusBar,
		Header:      header,
	}
}
