package ui_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

func TestIsLight(t *testing.T) {
	light, _ := ui.ResolveTheme("solarized-light", "", nil)
	dark, _ := ui.ResolveTheme("dracula", "", nil)
	if !ui.IsLight(light) || ui.IsLight(dark) {
		t.Errorf("IsLight: solarized-light %v, dracula %v", ui.IsLight(light), ui.IsLight(dark))
	}
}

// With no keys set, a dark ui.theme fills the dark side (with its
// overrides) and solarized-light the light side.
func TestResolveBrowserThemes_defaultsFromADarkTheme(t *testing.T) {
	active, _ := ui.ResolveTheme("dracula", "", map[string]string{"accent": "#123456"})
	th, warns := ui.ResolveBrowserThemes(active, "", "", "")
	if len(warns) != 0 {
		t.Fatalf("warns = %v", warns)
	}
	if th.Dark["background"] != "#282a36" || th.Dark["accent"] != "#123456" {
		t.Errorf("dark = %v", th.Dark)
	}
	if th.Light["background"] != "#fdf6e3" || th.Light["accent"] != "#2aa198" {
		t.Errorf("light = %v", th.Light)
	}
}

func TestResolveBrowserThemes_defaultsFromALightTheme(t *testing.T) {
	active, _ := ui.ResolveTheme("solarized-light", "", nil)
	th, _ := ui.ResolveBrowserThemes(active, "", "", "")
	if th.Light["background"] != "#fdf6e3" || th.Dark["background"] != "#002b36" {
		t.Errorf("got %v / %v", th.Light, th.Dark)
	}
}

// Named keys win; ui.colors overrides do not reach a side using another theme.
func TestResolveBrowserThemes_namedThemes(t *testing.T) {
	active, _ := ui.ResolveTheme("solarized-dark", "", map[string]string{"accent": "#123456"})
	th, warns := ui.ResolveBrowserThemes(active, "solarized-light", "nord", "")
	if len(warns) != 0 {
		t.Fatalf("warns = %v", warns)
	}
	if th.Dark["accent"] == "#123456" || th.Dark["background"] != "#2e3440" {
		t.Errorf("dark = %v", th.Dark)
	}
}

// A missing theme falls back to that side's default, with a warning naming the key.
func TestResolveBrowserThemes_missingThemeFallsBackPerSide(t *testing.T) {
	active, _ := ui.ResolveTheme("", "", nil)
	th, warns := ui.ResolveBrowserThemes(active, "nope", "", t.TempDir())
	if th.Light["background"] != "#fdf6e3" {
		t.Errorf("light fell back to %v", th.Light["background"])
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "ui.browser.light_theme") {
		t.Errorf("warns = %v", warns)
	}
}

// Names and "default" become hex; nothing else reaches the page.
func TestResolveBrowserThemes_colourNamesBecomeHex(t *testing.T) {
	active, _ := ui.ResolveTheme("dracula", "", map[string]string{"accent": "red", "dim": "default"})
	th, _ := ui.ResolveBrowserThemes(active, "", "", "")
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for role, c := range th.Dark {
		if !hex.MatchString(c) {
			t.Errorf("%s = %q", role, c)
		}
	}
	if th.Dark["dim"] != "#586e75" {
		t.Errorf("default dim = %q, want solarized-dark's", th.Dark["dim"])
	}
}

// A light theme with a transparent background (ui.colors background:
// default) is still light, and each side fills a colour it lacks from its
// own default theme: no dark colours on the light page, nor light on dark.
func TestResolveBrowserThemes_transparentLightThemeStaysLight(t *testing.T) {
	active, _ := ui.ResolveTheme("solarized-light", "", map[string]string{"background": "default"})
	if !ui.IsLight(active) {
		t.Fatal("a light theme with a default background counts as dark")
	}
	th, _ := ui.ResolveBrowserThemes(active, "", "", "")
	if th.Light["background"] != "#fdf6e3" || th.Light["header_bg"] != "#eee8d5" {
		t.Errorf("light = %v, want solarized-light's colours", th.Light)
	}
	if th.Dark["background"] != "#002b36" || th.Dark["header_bg"] != "#073642" {
		t.Errorf("dark = %v, want plain solarized-dark", th.Dark)
	}
}
