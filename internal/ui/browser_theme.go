package ui

import (
	"fmt"
	"math"

	"github.com/derailed/tcell/v2"
)

// browserRoles are the theme roles the browser page (b) is drawn with.
var browserRoles = []string{"background", "foreground", "header_bg", "label", "heading", "border", "warn", "accent", "dim"}

// The themes a side of the browser page falls back to.
const (
	defaultLightTheme = "solarized-light"
	defaultDarkTheme  = DefaultTheme
)

// BrowserThemes is the browser page's two palettes: role → "#rrggbb".
type BrowserThemes struct {
	Light, Dark map[string]string
}

// ResolveBrowserThemes picks the palettes of the browser page's Light and
// Dark modes. active is the TUI's resolved theme (ui.theme with ui.colors);
// lightName and darkName are ui.browser.light_theme and dark_theme. A side
// left empty uses active when its brightness matches, else solarized-light
// or solarized-dark; a side naming active's theme uses active, overrides
// included. A theme that cannot be loaded falls back to the side's default,
// with a warning. It never fails.
func ResolveBrowserThemes(active *Skin, lightName, darkName, userDir string) (BrowserThemes, []string) {
	var warns []string
	side := func(key, name, fallback string, light bool) map[string]string {
		switch {
		case name == "" && active != nil && IsLight(active) == light:
			return palette(active, fallback)
		case name == "":
			s, _ := ResolveTheme(fallback, userDir, nil)
			return palette(s, fallback)
		case active != nil && name == active.Name:
			return palette(active, fallback)
		}
		s, w := ResolveTheme(name, userDir, nil)
		if len(w) > 0 {
			warns = append(warns, fmt.Sprintf("ui.browser.%s: %s", key, w[0]))
			s, _ = ResolveTheme(fallback, userDir, nil)
		}
		return palette(s, fallback)
	}
	t := BrowserThemes{
		Light: side("light_theme", lightName, defaultLightTheme, true),
		Dark:  side("dark_theme", darkName, defaultDarkTheme, false),
	}
	return t, warns
}

// DefaultBrowserThemes is the page's palettes without any config:
// solarized-light and solarized-dark.
func DefaultBrowserThemes() BrowserThemes {
	def, _ := LoadSkin("")
	t, _ := ResolveBrowserThemes(def, "", "", "")
	return t
}

// IsLight reports whether a theme is light: the relative luminance of its
// background is above 0.5. A background without a colour of its own
// ("default": a see-through terminal) is judged by the text instead: dark
// text is a light theme. With neither, the theme counts as dark, like the
// default theme.
func IsLight(s *Skin) bool {
	if l, ok := luminance(s.ColorName("background")); ok {
		return l > 0.5
	}
	if l, ok := luminance(s.ColorName("foreground")); ok {
		return l < 0.5
	}
	return false
}

// luminance is a colour's relative luminance (WCAG); false when it has no RGB.
func luminance(c string) (float64, bool) {
	r, g, b := tcell.GetColor(c).RGB()
	if r < 0 {
		return 0, false
	}
	lin := func(c int32) float64 {
		v := float64(c) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b), true
}

// palette is s's browserRoles as "#rrggbb". A colour tcell cannot turn into
// RGB ("default") takes the colour of fallback, the side's own default
// theme, so a light page never gets a dark default and the other way round.
func palette(s *Skin, fallback string) map[string]string {
	def, _ := ResolveTheme(fallback, "", nil)
	out := make(map[string]string, len(browserRoles))
	for _, role := range browserRoles {
		hex := tcell.GetColor(s.ColorName(role)).Hex()
		if hex < 0 {
			hex = tcell.GetColor(def.ColorName(role)).Hex()
		}
		out[role] = fmt.Sprintf("#%06x", hex)
	}
	return out
}
