package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval.Duration != 2*time.Minute || cfg.Claude.Path != "claude" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Watch("zed/b").AutoRun = true
	cfg.Watch("amy/a")
	cfg.Watch("amy/a") // duplicate is a no-op
	cfg.PollInterval.Duration = 5 * time.Minute
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repos) != 2 || got.Repos[0].FullName != "amy/a" {
		t.Fatalf("repos not saved sorted: %+v", got.Repos)
	}
	if !got.Repo("ZED/B").AutoRun {
		t.Fatal("AutoRun lost or lookup not case-insensitive")
	}
	if !got.Repo("amy/a").Watches(KindDependabot) {
		t.Fatal("new watch should enable all kinds")
	}
	if got.PollInterval.Duration != 5*time.Minute {
		t.Fatalf("poll interval = %v", got.PollInterval)
	}

	got.Unwatch("amy/a")
	if got.Repo("amy/a") != nil {
		t.Fatal("unwatch failed")
	}
}

func TestWorkDir(t *testing.T) {
	cfg := Default()
	cfg.WorkspaceDir = "/ws"
	if got := cfg.WorkDir("o/r"); got != filepath.FromSlash("/ws/o/r") {
		t.Fatalf("got %s", got)
	}
	cfg.Watch("o/r").LocalPath = "/src/r"
	if got := cfg.WorkDir("o/r"); got != "/src/r" {
		t.Fatalf("got %s", got)
	}
}

func TestLoadValidatesAutoRules(t *testing.T) {
	tests := map[string]string{
		`{"auto_rules":{"dependabot_alert":{"min_severity":"high"}},
		  "repos":[{"full_name":"o/r","auto_rules":{"pull_request":{"skip_drafts":true,"labels":["bug"]}}}]}`: "",
		`{"auto_rules":{"pr":{}}}`:                                                    "unknown event kind",
		`{"auto_rules":{"issue":{"min_severity":"high"}}}`:                            "only applies to alerts",
		`{"auto_rules":{"dependabot_alert":{"min_severity":"severe"}}}`:               "unknown min_severity",
		`{"auto_rules":{"code_scanning_alert":{"labels":["x"]}}}`:                     "only apply to pull requests",
		`{"repos":[{"full_name":"o/r","auto_rules":{"issue":{"skip_drafts":true}}}]}`: "o/r auto_rules",
	}
	for raw, want := range tests {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", raw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got error %v, want one containing %q", raw, err, want)
		}
	}
}

func TestRulesFor(t *testing.T) {
	cfg := Default()
	r := Repo{FullName: "o/r"}
	if cfg.RulesFor(r) != nil {
		t.Fatal("no rules anywhere should return nil")
	}
	cfg.AutoRules = map[EventKind]Rule{KindIssue: {}}
	if _, ok := cfg.RulesFor(r)[KindIssue]; !ok {
		t.Fatal("repo without rules should inherit the global ones")
	}
	r.AutoRules = map[EventKind]Rule{KindPullRequest: {}}
	if _, ok := cfg.RulesFor(r)[KindIssue]; ok {
		t.Fatal("repo rules should replace the global ones")
	}
}
