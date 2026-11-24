package ui

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/derailed/tcell/v2"
	"gopkg.in/yaml.v3"
)

//go:embed themes/solarized-dark.yaml
var defaultSkinYAML []byte

// Skin holds color mappings for semantic UI roles.
type Skin struct {
	Name   string            `yaml:"name"`
	Colors map[string]string `yaml:"colors"`
}

// LoadSkin loads a skin from a YAML file. If path is empty, returns the
// embedded solarized-dark default.
func LoadSkin(path string) (*Skin, error) {
	data := defaultSkinYAML
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading skin %s: %w", path, err)
		}
	}
	var s Skin
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing skin: %w", err)
	}
	if s.Colors == nil {
		s.Colors = map[string]string{}
	}
	return &s, nil
}

// Color returns a tcell.Color for a semantic role, falling back to default.
func (s *Skin) Color(role string) tcell.Color {
	hex, ok := s.Colors[role]
	if !ok {
		return tcell.ColorDefault
	}
	return tcell.GetColor(hex)
}

// ColorName returns the raw color string for a semantic role for use in tview
// markup tags, falling back to "default" if the role is not defined.
func (s *Skin) ColorName(role string) string {
	if s.Colors == nil {
		return "default"
	}
	if c, ok := s.Colors[role]; ok && c != "" {
		return c
	}
	return "default"
}

// Style returns a tcell.Style with fg and bg from semantic roles.
func (s *Skin) Style(fg, bg string) tcell.Style {
	return tcell.StyleDefault.
		Foreground(s.Color(fg)).
		Background(s.Color(bg))
}
