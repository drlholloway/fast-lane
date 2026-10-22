package version

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestOutdated(t *testing.T) {
	built := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	before, after := built.Add(-time.Hour), built.Add(time.Hour)
	tests := []struct {
		name      string
		current   string
		built     time.Time
		latest    string
		published time.Time
		want      bool
	}{
		{"newer patch", "v0.2.0", time.Time{}, "v0.2.1", after, true},
		{"newer minor", "v0.2.9", time.Time{}, "v0.10.0", after, true},
		{"same", "v0.2.0", time.Time{}, "v0.2.0", after, false},
		{"older", "v0.3.0", time.Time{}, "v0.2.0", after, false},
		{"release after its rc", "v1.3.0-rc.1", time.Time{}, "v1.3.0", after, true},
		{"rc after its release", "v1.3.0", time.Time{}, "v1.3.0-rc.1", after, false},
		{"unreadable tag", "v0.2.0", time.Time{}, "nightly", after, false},
		// A release build ignores dates: the tag decides.
		{"release ignores dates", "v0.2.0", built, "v0.2.0", after, false},
		{"checkout before the release", "dev (dbc40f2, 2026-10-22)", built, "v0.2.0", after, true},
		{"checkout after the release", "dev (dbc40f2, 2026-10-22)", built, "v0.2.0", before, false},
		{"checkout without a date", "dev", time.Time{}, "v0.2.0", after, false},
	}
	for _, tt := range tests {
		if got := outdated(tt.current, tt.built, tt.latest, tt.published); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestBuiltAt(t *testing.T) {
	want := time.Date(2026, 10, 21, 21, 9, 42, 0, time.UTC)
	for _, info := range []*debug.BuildInfo{
		{Main: debug.Module{Version: "v0.0.0-20261021210942-f75d38714b04+dirty"}},
		{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.time", Value: "2026-10-21T21:09:42Z"}}},
	} {
		if got := builtAt(info); !got.Equal(want) {
			t.Errorf("%s: got %v, want %v", info.Main.Version, got, want)
		}
	}
	if got := builtAt(nil); !got.IsZero() {
		t.Errorf("no build info: got %v", got)
	}
}
