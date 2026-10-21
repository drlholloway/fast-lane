// Package version reports which build of Fast Lane is running.
package version

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// Version can be set at build time with
// -ldflags "-X github.com/drlholloway/fast-lane/internal/version.Version=v1.2.3".
var Version = ""

// String returns the release version, such as v1.2.3, or for a build from a
// checkout "dev (f75d387, 2026-10-21)", with ", modified" when the checkout
// had uncommitted changes.
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return fromBuildInfo(info)
}

// pseudoRE matches Go pseudo-versions such as
// v0.0.0-20261021210942-f75d38714b04+dirty, which Go records for builds of
// untagged commits, both from a checkout and through go install.
var pseudoRE = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})\d{6}-([0-9a-f]{12})(\+dirty)?$`)

func fromBuildInfo(info *debug.BuildInfo) string {
	v := info.Main.Version
	if m := pseudoRE.FindStringSubmatch(v); m != nil {
		parts := []string{m[4][:7], m[1] + "-" + m[2] + "-" + m[3]}
		if m[5] != "" {
			parts = append(parts, "modified")
		}
		return "dev (" + strings.Join(parts, ", ") + ")"
	}
	// A release, such as go install …@v1.2.3.
	if v != "" && v != "(devel)" {
		return v
	}
	var rev, date string
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			date, _, _ = strings.Cut(s.Value, "T")
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	parts := []string{rev[:min(7, len(rev))]}
	if date != "" {
		parts = append(parts, date)
	}
	if modified {
		parts = append(parts, "modified")
	}
	return "dev (" + strings.Join(parts, ", ") + ")"
}
