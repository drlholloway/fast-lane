package version

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	vcs := func(rev, time, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.time", Value: time}, {Key: "vcs.modified", Value: modified}}
	}
	tests := []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"go install", debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
		{"checkout", debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: vcs("f75d38712345", "2026-10-21T18:04:00Z", "false")}, "dev (f75d387, 2026-10-21)"},
		{"modified checkout", debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: vcs("f75d38712345", "2026-10-21T18:04:00Z", "true")}, "dev (f75d387, 2026-10-21, modified)"},
		{"no vcs", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"pseudo from a checkout", debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261021210942-f75d38714b04+dirty"}},
			"dev (f75d387, 2026-10-21, modified)"},
		{"pseudo from go install", debug.BuildInfo{Main: debug.Module{Version: "v1.2.4-0.20261021210942-f75d38714b04"}},
			"dev (f75d387, 2026-10-21)"},
		{"prerelease", debug.BuildInfo{Main: debug.Module{Version: "v1.3.0-rc.1"}}, "v1.3.0-rc.1"},
	}
	for _, tt := range tests {
		if got := fromBuildInfo(&tt.info); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestLdflagsWin(t *testing.T) {
	Version = "v9.9.9"
	defer func() { Version = "" }()
	if got := String(); got != "v9.9.9" {
		t.Fatalf("got %q", got)
	}
}
