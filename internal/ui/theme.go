package ui

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/derailed/tcell/v2"
	"gopkg.in/yaml.v3"
)

// DefaultTheme is used when no theme is configured or the configured one
// cannot be loaded.
const DefaultTheme = "solarized-dark"

//go:embed themes/*.yaml
var builtinThemes embed.FS

// BuiltinThemes returns the names of the themes compiled into m9s.
func BuiltinThemes() []string {
	entries, _ := builtinThemes.ReadDir("themes")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

// BuiltinThemeYAML returns the raw YAML of a built-in theme.
func BuiltinThemeYAML(name string) ([]byte, error) {
	return builtinThemes.ReadFile("themes/" + name + ".yaml")
}

// ResolveTheme builds the active skin. It never fails: problems are returned
// as warnings and the default is used in their place.
//
// Lookup order for name: built-in theme, then <userDir>/<name>.yaml.
// Roles the theme leaves out keep the default theme's colour. overrides are
// applied last; unknown roles and invalid colours are skipped with a warning.
func ResolveTheme(name, userDir string, overrides map[string]string) (*Skin, []string) {
	var warns []string
	skin, _ := LoadSkin("") // embedded default, cannot fail

	if name != "" && name != DefaultTheme {
		theme, err := loadNamedTheme(name, userDir)
		if err != nil {
			warns = append(warns, err.Error())
		} else {
			skin.Name = name
			for role, c := range theme.Colors {
				skin.Colors[role] = c
			}
		}
	}

	roles := make([]string, 0, len(overrides))
	for role := range overrides {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		c := overrides[role]
		if _, known := skin.Colors[role]; !known {
			warns = append(warns, fmt.Sprintf("ui.colors: unknown role %q", role))
			continue
		}
		if !validColor(c) {
			warns = append(warns, fmt.Sprintf("ui.colors.%s: invalid colour %q", role, c))
			continue
		}
		skin.Colors[role] = c
	}
	return skin, warns
}

func loadNamedTheme(name, userDir string) (*Skin, error) {
	data, err := BuiltinThemeYAML(name)
	if err != nil {
		if userDir == "" {
			return nil, fmt.Errorf("theme %q not found (built-in: %s)", name, strings.Join(BuiltinThemes(), ", "))
		}
		path := filepath.Join(userDir, name+".yaml")
		if data, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("theme %q not found (built-in: %s; custom: %s)",
				name, strings.Join(BuiltinThemes(), ", "), path)
		}
	}
	var s Skin
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("theme %q: %w", name, err)
	}
	return &s, nil
}

func validColor(c string) bool {
	return c == "default" || tcell.GetColor(c) != tcell.ColorDefault
}
