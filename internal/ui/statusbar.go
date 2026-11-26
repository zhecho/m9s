package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/tview"
)

// KeyHint is a single key+description pair for the status bar.
type KeyHint struct {
	Key  string
	Desc string
}

// StatusBar is the one-row bottom bar: breadcrumbs on the left, transient
// flash messages on the right.
type StatusBar struct {
	*tview.Flex
	crumbs     *tview.TextView
	crumbsText string
	flash      *tview.TextView
	flashTimer *time.Timer
	skin       *Skin
	app        *tview.Application
	logText    func(msg string) string // how a message is worded in the log
}

// SetLogText sets how Info and Error word their message in the log (the
// account it is about in front); the bar shows it as it is.
func (sb *StatusBar) SetLogText(fn func(msg string) string) { sb.logText = fn }

func (sb *StatusBar) forLog(msg string) string {
	if sb.logText == nil {
		return msg
	}
	return sb.logText(msg)
}

// NewStatusBar creates the one-row status bar.
func NewStatusBar(skin *Skin, app *tview.Application) *StatusBar {
	makeCell := func() *tview.TextView {
		tv := tview.NewTextView()
		tv.SetDynamicColors(true)
		tv.SetBorder(false)
		tv.SetBackgroundColor(skin.Color("statusbar_bg"))
		return tv
	}

	sb := &StatusBar{
		Flex:   tview.NewFlex().SetDirection(tview.FlexColumn),
		crumbs: makeCell(),
		flash:  makeCell(),
		skin:   skin,
		app:    app,
	}
	sb.flash.SetTextAlign(tview.AlignRight)
	sb.AddItem(sb.crumbs, 0, 1, false).
		AddItem(sb.flash, 0, 2, false)
	return sb
}

// SetCrumbs shows parts as k9s-style breadcrumb chips: <a> <b>. Empty parts
// are skipped.
func (sb *StatusBar) SetCrumbs(parts ...string) {
	fg := sb.skin.ColorName("selected_fg")
	bg := sb.skin.ColorName("accent")
	var plain, chips []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		plain = append(plain, "<"+p+">")
		chips = append(chips, fmt.Sprintf("[%s:%s:b] <%s> [-:-:-]", fg, bg, tview.Escape(p)))
	}
	sb.crumbsText = strings.Join(plain, " ")
	sb.crumbs.SetText(strings.Join(chips, " "))
}

// CrumbsText returns the breadcrumbs as plain text (for tests).
func (sb *StatusBar) CrumbsText() string { return sb.crumbsText }

func (sb *StatusBar) setFlash(msg, color string, dur time.Duration) {
	if sb.flashTimer != nil {
		sb.flashTimer.Stop()
	}
	sb.flash.SetText(fmt.Sprintf("[%s::]%s[-:-:-]", color, tview.Escape(msg)))
	if sb.app != nil {
		sb.flashTimer = time.AfterFunc(dur, func() {
			sb.app.QueueUpdateDraw(func() {
				sb.flash.SetText("")
			})
		})
	}
}

// Info shows a green message in the flash row that clears after 3s, and
// logs it as SetLogText words it.
func (sb *StatusBar) Info(msg string) {
	AppLog.Info("%s", sb.forLog(msg))
	sb.setFlash(msg, sb.skin.Colors["flash_ok"], 3*time.Second)
}

// Error shows a red message in the flash row that clears after 5s, and
// logs it as SetLogText words it.
func (sb *StatusBar) Error(msg string) {
	AppLog.Error("%s", sb.forLog(msg))
	sb.setFlash(msg, sb.skin.Colors["flash_err"], 5*time.Second)
}

// InfoApp is Info for a message about m9s itself, not an account: it is
// logged as it is.
func (sb *StatusBar) InfoApp(msg string) {
	AppLog.Info("%s", msg)
	sb.setFlash(msg, sb.skin.Colors["flash_ok"], 3*time.Second)
}

// Progress shows a green message in the flash row without logging it:
// work in progress, said again every second, would flood the log.
func (sb *StatusBar) Progress(msg string) {
	sb.setFlash(msg, sb.skin.Colors["flash_ok"], 3*time.Second)
}

// ErrorApp is Error for a message about m9s itself, not an account.
func (sb *StatusBar) ErrorApp(msg string) {
	AppLog.Error("%s", msg)
	sb.setFlash(msg, sb.skin.Colors["flash_err"], 5*time.Second)
}

// FlashText returns the current flash row text (for tests).
func (sb *StatusBar) FlashText() string { return sb.flash.GetText(true) }
