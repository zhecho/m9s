// Package version reports the m9s build version.
package version

import "runtime/debug"

// Version is set at build time with
// -ldflags "-X github.com/zhecho/m9s/internal/version.Version=v0.0.1"; empty means unset.
var Version string

// String returns the linker-set version, else the module version Go stamps
// from the VCS tag (go build in a tagged checkout), else "dev".
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
