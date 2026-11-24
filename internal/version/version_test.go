package version

import "testing"

func TestStringPrefersLinkerValue(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v9.9.9"
	if got := String(); got != "v9.9.9" {
		t.Errorf("String() = %q, want v9.9.9", got)
	}
}

func TestStringFallsBackToNonEmpty(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = ""
	if got := String(); got == "" {
		t.Error("String() is empty without a linker value")
	}
}
