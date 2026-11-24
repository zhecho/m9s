package view

import (
	"context"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/ui"
)

// CredWindowDeps is what the startup window needs.
type CredWindowDeps struct {
	Skin     *ui.Skin
	Accounts []string // in config order
	// Services names each account's two services where they are not imap
	// and smtp (gmail: client and token).
	Services map[string][]string

	// Run does the lookup. It calls onUpdate and onNotice from its own
	// goroutine, and returns the results and whether the user was kept
	// waiting. It must return soon after ctx is cancelled.
	Run func(ctx context.Context, onUpdate func(creds.Update), onNotice func(string)) (results []creds.Result, waited bool)
	// Skip is called when Esc is pressed while Run is in progress.
	Skip func()

	Screen tcell.Screen                         // nil: the real terminal, taken and given back
	After  func(time.Duration) <-chan time.Time // nil: time.After
}

type credRunResult struct {
	results []creds.Result
	waited  bool
}

// ShowCredentials shows the grid of password sources while the lookup runs,
// before the main screen exists.
//
// It returns as soon as the lookup is over if nothing went wrong, so a
// normal start only flashes it. If something failed it stays for the
// outcome's pause, or until a key is pressed. quit is true if the user
// pressed q or Ctrl-C: m9s should then exit, not start.
func ShowCredentials(d CredWindowDeps) (results []creds.Result, outcome CredOutcome, quit bool) {
	grid := NewCredGrid(d.Accounts)
	for a, svc := range d.Services {
		grid.SetServices(a, svc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after := d.After
	if after == nil {
		after = time.After
	}

	screen := d.Screen
	if screen == nil {
		s, err := tcell.NewScreen()
		if err == nil {
			err = s.Init()
		}
		if err != nil {
			// No terminal to draw on: do the lookup without a window.
			res, waited := d.Run(ctx, func(creds.Update) {}, func(string) {})
			return res, grid.Finish(res, waited), false
		}
		screen = s
		defer screen.Fini()
	}

	// The lookup, the keyboard and the clock all feed one loop.
	dirty := make(chan struct{}, 1)
	redraw := func() {
		select {
		case dirty <- struct{}{}:
		default:
		}
	}
	done := make(chan credRunResult, 1)
	go func() {
		res, waited := d.Run(ctx,
			func(u creds.Update) { grid.Apply(u); redraw() },
			func(text string) { grid.SetNotice(text); redraw() })
		done <- credRunResult{res, waited}
	}()

	stop := make(chan struct{})
	defer close(stop)
	events := make(chan tcell.Event)
	go func() {
		for {
			ev := screen.PollEvent()
			if ev == nil {
				return // the screen is gone
			}
			select {
			case events <- ev:
			case <-stop:
				return
			}
		}
	}()

	running := true
	var tick <-chan time.Time
	left := 0
	for {
		drawCredWindow(screen, d.Skin, grid)
		select {
		case ev := <-events:
			switch ev := ev.(type) {
			case *tcell.EventResize:
				screen.Sync()
			case *tcell.EventKey:
				switch {
				case ev.Key() == tcell.KeyCtrlC || (ev.Key() == tcell.KeyRune && ev.Rune() == 'q'):
					return results, outcome, true
				case running:
					if ev.Key() == tcell.KeyEscape && d.Skip != nil {
						d.Skip()
					}
				default:
					return results, outcome, false // any key ends the pause
				}
			}
		case <-dirty:
		case r := <-done:
			running = false
			results = r.results
			outcome = grid.Finish(r.results, r.waited)
			if outcome.Pause == 0 {
				return results, outcome, false
			}
			left = int(outcome.Pause / time.Second)
			tick = after(time.Second)
		case <-tick:
			left--
			if left <= 0 {
				return results, outcome, false
			}
			grid.Tick(left)
			tick = after(time.Second)
		}
	}
}

const credWindowTitle = " m9s · getting passwords "

// drawCredWindow paints the grid in a box as wide as it needs, at the top
// left of the screen.
func drawCredWindow(screen tcell.Screen, skin *ui.Skin, grid *CredGrid) {
	w, h := screen.Size()
	base := tcell.StyleDefault.Background(skin.Color("background")).Foreground(skin.Color("foreground"))
	frame := base.Foreground(skin.Color("border"))
	title := base.Foreground(skin.Color("accent")).Bold(true)
	marks := map[rune]tcell.Style{
		'✔': base.Foreground(skin.Color("flash_ok")).Bold(true),
		'✗': base.Foreground(skin.Color("flash_err")).Bold(true),
		'…': base.Foreground(skin.Color("accent")).Bold(true),
		'–': base.Foreground(skin.Color("dim")),
		'·': base.Foreground(skin.Color("dim")),
	}

	screen.SetStyle(base)
	screen.Clear()
	defer screen.Show()
	if w < 4 || h < 3 {
		return
	}

	lines := grid.LinesIn(w-4, h-2)
	inner := 0
	for _, l := range lines {
		inner = max(inner, len([]rune(l)))
	}
	inner = min(max(inner, len([]rune(credWindowTitle))+2), w-4)
	rows := min(len(lines), h-2)
	right, bottom := inner+3, rows+1

	put := func(x, y int, r rune, st tcell.Style) {
		if x >= 0 && x < w && y >= 0 && y < h {
			screen.SetContent(x, y, r, nil, st)
		}
	}
	for x := 1; x < right; x++ {
		put(x, 0, '─', frame)
		put(x, bottom, '─', frame)
	}
	for y := 1; y < bottom; y++ {
		put(0, y, '│', frame)
		put(right, y, '│', frame)
	}
	put(0, 0, '┌', frame)
	put(right, 0, '┐', frame)
	put(0, bottom, '└', frame)
	put(right, bottom, '┘', frame)
	for i, r := range []rune(credWindowTitle) {
		if 2+i < right {
			put(2+i, 0, r, title)
		}
	}
	for y := 0; y < rows; y++ {
		for x, r := range []rune(lines[y]) {
			st, ok := marks[r]
			if !ok {
				st = base
			}
			put(2+x, 1+y, r, st)
		}
	}
}
