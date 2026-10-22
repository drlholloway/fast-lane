package version

import (
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Outdated reports whether the release tagged latest, published at
// published, is newer than this build. Release builds compare version
// numbers; builds from a checkout compare the release's date with their
// commit's.
func Outdated(latest string, published time.Time) bool {
	info, _ := debug.ReadBuildInfo()
	return outdated(String(), builtAt(info), latest, published)
}

func outdated(current string, built time.Time, latest string, published time.Time) bool {
	if cur, ok := parse(current); ok {
		l, ok := parse(latest)
		return ok && l.newer(cur)
	}
	return !built.IsZero() && published.After(built)
}

type semver struct {
	n   [3]int
	pre string
}

var semverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func parse(s string) (semver, bool) {
	m := semverRE.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := range v.n {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre = m[4]
	return v, true
}

// newer reports whether v comes after w. A prerelease such as v1.3.0-rc.1
// comes before v1.3.0; prereleases of the same version compare as text.
func (v semver) newer(w semver) bool {
	for i := range v.n {
		if v.n[i] != w.n[i] {
			return v.n[i] > w.n[i]
		}
	}
	switch {
	case v.pre == w.pre:
		return false
	case v.pre == "":
		return true
	case w.pre == "":
		return false
	}
	return v.pre > w.pre
}

// pseudoTimeRE matches the commit time in a Go pseudo-version.
var pseudoTimeRE = regexp.MustCompile(`(\d{14})-[0-9a-f]{12}(\+dirty)?$`)

// builtAt returns the time of the commit a build was made from, or zero
// when the build doesn't record one.
func builtAt(info *debug.BuildInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	if m := pseudoTimeRE.FindStringSubmatch(info.Main.Version); m != nil {
		t, _ := time.Parse("20060102150405", m[1])
		return t
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.time" {
			t, _ := time.Parse(time.RFC3339, strings.TrimSpace(s.Value))
			return t
		}
	}
	return time.Time{}
}
