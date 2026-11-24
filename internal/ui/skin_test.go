package ui_test

import (
	"os"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/ui"
)

func TestDefaultSkin(t *testing.T) {
	s, err := ui.LoadSkin("")
	if err != nil {
		t.Fatalf("LoadSkin default: %v", err)
	}
	if s.Name != "solarized-dark" {
		t.Errorf("want solarized-dark, got %q", s.Name)
	}
	if s.Colors["background"] == "" {
		t.Error("background color not set")
	}
}

func TestLoadSkinFromFile(t *testing.T) {
	content := []byte("name: test\ncolors:\n  background: \"#000000\"\n")
	f := t.TempDir() + "/skin.yaml"
	if err := os.WriteFile(f, content, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ui.LoadSkin(f)
	if err != nil {
		t.Fatalf("LoadSkin from file: %v", err)
	}
	if s.Name != "test" {
		t.Errorf("want test, got %q", s.Name)
	}
}

func TestSkinColorName(t *testing.T) {
	s, _ := ui.LoadSkin("")
	if s.ColorName("background") == "default" {
		t.Error("known role should not fall back to default")
	}
	if s.ColorName("nonexistent_role") != "default" {
		t.Error("unknown role should return default")
	}
}

func TestLoadSkin_notFound(t *testing.T) {
	_, err := ui.LoadSkin("/no/such/skin.yaml")
	if err == nil {
		t.Error("expected error loading nonexistent skin file")
	}
}

func TestLoadSkin_invalidYAML(t *testing.T) {
	f := t.TempDir() + "/bad.yaml"
	if err := os.WriteFile(f, []byte("{\ninvalid"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ui.LoadSkin(f)
	if err == nil {
		t.Error("expected error on invalid YAML skin")
	}
}

func TestSkinColor_unknownRole(t *testing.T) {
	s := &ui.Skin{Colors: map[string]string{}}
	if s.Color("nonexistent") != tcell.ColorDefault {
		t.Error("unknown color role should return tcell.ColorDefault")
	}
}

func TestSkinColorName_nilColors(t *testing.T) {
	s := &ui.Skin{} // Colors is nil
	if s.ColorName("any") != "default" {
		t.Error("nil Colors map should return 'default'")
	}
}

func TestSkinStyle(t *testing.T) {
	s, _ := ui.LoadSkin("")
	style := s.Style("foreground", "background")
	// Style must not panic and must return a valid tcell.Style (zero check via Decompose).
	fg, bg, _ := style.Decompose()
	if fg == bg && fg == tcell.ColorDefault {
		t.Error("expected non-default colors in style")
	}
}
