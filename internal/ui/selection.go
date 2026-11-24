package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// Rect is a screen area: column x, row y, w columns wide and h rows high.
type Rect struct{ X, Y, W, H int }

func (r Rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Selector lets the user select any text on screen by dragging with the left
// mouse button. The selection stays highlighted and, when the button is
// released, its text is copied to the clipboard. It stays inside the pane the
// drag started in, so selecting a mail body does not pick up the folder tree.
// A click without dragging reaches the panes as before.
type Selector struct {
	// Regions returns the panes on screen; a drag is kept inside the one it
	// starts in (the whole screen when it starts in none).
	Regions func() []Rect
	// Busy reports whether a pane took the mouse down for its own drag (the
	// border between mail list and preview); no selection starts then.
	Busy func() bool
	// Copy puts text on the clipboard. Defaults to CopyToClipboard.
	Copy func(text string) error
	// OnCopied reports a finished selection.
	OnCopied func(text string, err error)

	screen         func() (w, h int)
	pressed        bool // left button down, no selection yet
	active         bool // dragging out a selection
	shown          bool // a selection is highlighted
	area           Rect
	ax, ay, cx, cy int        // anchor and current cell
	last           cellReader // reads the last drawn screen
}

// Install hooks the selector into app's mouse, key and drawing handling.
// Call it after the app's own input capture is set.
func (s *Selector) Install(app *tview.Application) {
	if s.Copy == nil {
		s.Copy = CopyToClipboard
	}
	app.SetMouseCapture(s.mouse)
	keys := app.GetInputCapture()
	app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		s.shown = false // the text under the highlight is about to change
		if keys != nil {
			return keys(ev)
		}
		return ev
	})
	after := app.GetAfterDrawFunc()
	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		if after != nil {
			after(screen)
		}
		s.screen = screen.Size
		s.highlight(screen)
	})
}

// mouse is the tview mouse capture: it returns nil for events it consumes.
func (s *Selector) mouse(ev *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
	if ev == nil {
		// tview fires several actions per terminal event (a move, then a
		// release) and hands the later ones the nil we returned for an
		// earlier one. The pointer is where the consumed move left it.
		if action == tview.MouseLeftUp {
			s.release()
		}
		return nil, action
	}
	x, y := ev.Position()
	switch action {
	case tview.MouseLeftDown:
		s.shown, s.active = false, false
		s.pressed = true
		s.ax, s.ay, s.cx, s.cy = x, y, x, y
		s.area = s.regionAt(x, y)
		return ev, action
	case tview.MouseMove:
		if s.pressed && !s.active && (x != s.ax || y != s.ay) {
			if ev.Buttons()&tcell.ButtonPrimary == 0 || (s.Busy != nil && s.Busy()) {
				s.pressed = false
				return ev, action
			}
			s.active, s.shown = true, true
		}
		if s.active {
			s.cx, s.cy = s.clamp(x, y)
			return nil, action
		}
	case tview.MouseLeftUp:
		if s.active {
			s.cx, s.cy = s.clamp(x, y)
			s.release()
			return nil, action
		}
		s.pressed = false
	}
	return ev, action
}

// release ends a drag and copies the selected text.
func (s *Selector) release() {
	s.pressed = false
	if !s.active {
		return
	}
	s.active = false
	text := s.Text(s.cellAt)
	if text != "" {
		err := s.Copy(text)
		if s.OnCopied != nil {
			s.OnCopied(text, err)
		}
	}
}

func (s *Selector) regionAt(x, y int) Rect {
	if s.Regions != nil {
		for _, r := range s.Regions() {
			if r.W > 0 && r.H > 0 && r.contains(x, y) {
				return r
			}
		}
	}
	w, h := 1<<15, 1<<15
	if s.screen != nil {
		w, h = s.screen()
	}
	return Rect{0, 0, w, h}
}

func (s *Selector) clamp(x, y int) (int, int) {
	a := s.area
	return min(max(x, a.X), a.X+a.W-1), min(max(y, a.Y), a.Y+a.H-1)
}

// span returns the selected columns of row y as [from, to], or ok=false.
// Like a terminal: the first row from the anchor on, middle rows whole, the
// last row up to the pointer, all within the pane.
func (s *Selector) span(y int) (from, to int, ok bool) {
	x0, y0, x1, y1 := s.ax, s.ay, s.cx, s.cy
	if y1 < y0 || (y1 == y0 && x1 < x0) {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	if y < y0 || y > y1 {
		return 0, 0, false
	}
	from, to = s.area.X, s.area.X+s.area.W-1
	if y == y0 {
		from = x0
	}
	if y == y1 {
		to = x1
	}
	return from, to, from <= to
}

// cellReader returns the text in a screen cell and how many columns it takes.
type cellReader func(x, y int) (string, int)

// Text returns the selected text, read from the screen through read: rows
// joined with newlines, trailing blanks dropped.
func (s *Selector) Text(read cellReader) string {
	if !s.shown && !s.active {
		return ""
	}
	y0, y1 := min(s.ay, s.cy), max(s.ay, s.cy)
	var lines []string
	for y := y0; y <= y1; y++ {
		from, to, ok := s.span(y)
		if !ok {
			continue
		}
		var b strings.Builder
		for x := from; x <= to; {
			str, w := read(x, y)
			b.WriteString(str)
			x += max(w, 1)
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// cellAt reads the last drawn screen; set by highlight.
func (s *Selector) cellAt(x, y int) (string, int) {
	if s.last == nil {
		return "", 1
	}
	return s.last(x, y)
}

func (s *Selector) highlight(screen tcell.Screen) {
	s.last = func(x, y int) (string, int) {
		mainc, combc, _, w := screen.GetContent(x, y)
		if mainc == 0 {
			return " ", 1
		}
		return string(mainc) + string(combc), w
	}
	if !s.shown {
		return
	}
	for y := min(s.ay, s.cy); y <= max(s.ay, s.cy); y++ {
		from, to, ok := s.span(y)
		if !ok {
			continue
		}
		for x := from; x <= to; {
			mainc, combc, style, w := screen.GetContent(x, y)
			screen.SetContent(x, y, mainc, combc, style.Reverse(true))
			x += max(w, 1) // the second half of a wide character goes with it
		}
	}
}

// CopyToClipboard puts text on the system clipboard with the platform's
// copy command, or, when there is none (an SSH session), asks the terminal
// to do it with an OSC 52 escape.
func CopyToClipboard(text string) error {
	for _, c := range clipboardCommands() {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("no clipboard: %w", err)
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}

func clipboardCommands() [][]string {
	if runtime.GOOS == "darwin" {
		return [][]string{{"pbcopy"}}
	}
	if os.Getenv("SSH_TTY") != "" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil // the clipboard is on the other end: OSC 52
	}
	return [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
}
