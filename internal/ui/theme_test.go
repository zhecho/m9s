package ui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

func TestResolveTheme_emptyIsSolarizedDark(t *testing.T) {
	s, warns := ui.ResolveTheme("", "", nil)
	if s.Name != "solarized-dark" || len(warns) != 0 {
		t.Errorf("got %q, warnings %v", s.Name, warns)
	}
}

func TestResolveTheme_builtin(t *testing.T) {
	s, warns := ui.ResolveTheme("dracula", "", nil)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if s.Name != "dracula" || s.ColorName("background") != "#282a36" {
		t.Errorf("got name %q background %q", s.Name, s.ColorName("background"))
	}
}

func TestBuiltinThemes_defineEveryRole(t *testing.T) {
	def, _ := ui.LoadSkin("")
	names := ui.BuiltinThemes()
	if len(names) < 6 {
		t.Fatalf("want at least 6 built-in themes, got %v", names)
	}
	for _, name := range names {
		_, warns := ui.ResolveTheme(name, "", nil)
		if len(warns) != 0 {
			t.Errorf("%s: warnings %v", name, warns)
		}
		raw, err := ui.BuiltinThemeYAML(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for role := range def.Colors {
			if !strings.Contains(string(raw), role+":") {
				t.Errorf("%s: missing role %q", name, role)
			}
		}
	}
}

func TestResolveTheme_userThemeFallsBackPerRole(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte("name: mine\ncolors:\n  background: \"#101010\"\n"), 0600)
	def, _ := ui.LoadSkin("")

	s, warns := ui.ResolveTheme("mine", dir, nil)

	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if s.ColorName("background") != "#101010" {
		t.Errorf("background = %q", s.ColorName("background"))
	}
	if s.ColorName("accent") != def.Colors["accent"] {
		t.Errorf("accent should fall back to default, got %q", s.ColorName("accent"))
	}
}

func TestResolveTheme_unknownWarnsAndUsesDefault(t *testing.T) {
	s, warns := ui.ResolveTheme("nope", t.TempDir(), nil)
	if s.Name != "solarized-dark" {
		t.Errorf("want fallback to solarized-dark, got %q", s.Name)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "nope") {
		t.Errorf("want one warning naming the theme, got %v", warns)
	}
}

func TestResolveTheme_overrides(t *testing.T) {
	s, warns := ui.ResolveTheme("nord", "", map[string]string{"accent": "#ff0000", "flash_err": "red"})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if s.ColorName("accent") != "#ff0000" || s.ColorName("flash_err") != "red" {
		t.Errorf("overrides not applied: accent %q flash_err %q", s.ColorName("accent"), s.ColorName("flash_err"))
	}
}

func TestResolveTheme_badOverridesWarnAndAreIgnored(t *testing.T) {
	base, _ := ui.ResolveTheme("nord", "", nil)
	s, warns := ui.ResolveTheme("nord", "", map[string]string{"accent": "notacolor", "bogus_role": "#fff000"})
	if len(warns) != 2 {
		t.Errorf("want 2 warnings, got %v", warns)
	}
	if s.ColorName("accent") != base.ColorName("accent") {
		t.Errorf("invalid color should be ignored, accent = %q", s.ColorName("accent"))
	}
	if _, ok := s.Colors["bogus_role"]; ok {
		t.Error("unknown role should not be added")
	}
}
