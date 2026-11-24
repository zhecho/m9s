package ui

import (
	"fmt"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// PromptHeight is the number of rows the prompt box occupies: a border with
// one text row.
const PromptHeight = 3

// PromptConfig describes one use of the prompt.
type PromptConfig struct {
	Label    string                           // "command" or "filter"; shown as the box title
	Prefix   string                           // ":" or "/" drawn before the text
	Suggest  func(text string) string         // full completion for text, or ""; may be nil
	OnChange func(text string)                // after every edit; may be nil
	OnDone   func(text string, accepted bool) // Enter → accepted true; Esc → false; may be nil
}

// Prompt is a one-line input with inline ghost-text completion, in the style
// of the k9s command bar. It handles all of its own keys.
type Prompt struct {
	*tview.TextView
	skin *Skin
	cfg  PromptConfig
	text []rune
}

// NewPrompt creates a closed prompt.
func NewPrompt(skin *Skin) *Prompt {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetWrap(false)
	tv.SetBorder(true)
	tv.SetBorderColor(skin.Color("accent"))
	tv.SetTitleColor(skin.Color("accent"))
	tv.SetBackgroundColor(skin.Color("background"))
	return &Prompt{TextView: tv, skin: skin}
}

// Open starts a new prompt session with empty text.
func (p *Prompt) Open(cfg PromptConfig) {
	p.cfg = cfg
	p.text = p.text[:0]
	p.SetTitle(" " + cfg.Label + " ")
	p.render()
}

// Text returns what has been typed.
func (p *Prompt) Text() string { return string(p.text) }

// Ghost returns the dim completion remainder currently offered, or "".
func (p *Prompt) Ghost() string {
	if p.cfg.Suggest == nil || len(p.text) == 0 {
		return ""
	}
	suggestion := []rune(p.cfg.Suggest(p.Text()))
	n := len(p.text) // compare by rune: folder names are not always ASCII
	if len(suggestion) <= n || !strings.EqualFold(string(suggestion[:n]), p.Text()) {
		return ""
	}
	return string(suggestion[n:])
}

func (p *Prompt) render() {
	p.SetText(fmt.Sprintf("[%s::b]%s[-:-:-][%s::]%s[-:-:-][%s::]%s[-:-:-]",
		p.skin.ColorName("accent"), tview.Escape(p.cfg.Prefix),
		p.skin.ColorName("foreground"), tview.Escape(p.Text()),
		p.skin.ColorName("dim"), tview.Escape(p.Ghost())))
}

func (p *Prompt) changed() {
	p.render()
	if p.cfg.OnChange != nil {
		p.cfg.OnChange(p.Text())
	}
}

// InputHandler handles every key itself: nothing is passed on.
func (p *Prompt) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return p.WrapInputHandler(func(event *tcell.EventKey, _ func(tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyRune:
			p.text = append(p.text, event.Rune())
			p.changed()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(p.text) > 0 {
				p.text = p.text[:len(p.text)-1]
				p.changed()
			}
		case tcell.KeyTab:
			if p.cfg.Suggest == nil {
				return
			}
			if s := p.cfg.Suggest(p.Text()); s != "" && s != p.Text() {
				p.text = []rune(s)
				p.changed()
			}
		case tcell.KeyEnter:
			p.finish(true)
		case tcell.KeyEscape:
			p.finish(false)
		}
	})
}

func (p *Prompt) finish(accepted bool) {
	if p.cfg.OnDone != nil {
		p.cfg.OnDone(p.Text(), accepted)
	}
}
