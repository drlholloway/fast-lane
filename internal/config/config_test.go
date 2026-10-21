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
		`{"auto_rules":{"pr":{}}}`:                                                                   "unknown event kind",
		`{"auto_rules":{"issue":{"min_severity":"high"}}}`:                                           "only applies to alerts",
		`{"auto_rules":{"dependabot_alert":{"min_severity":"severe"}}}`:                              "unknown min_severity",
		`{"auto_rules":{"code_scanning_alert":{"labels":["x"]}}}`:                                    "only apply to pull requests",
		`{"repos":[{"full_name":"o/r","auto_rules":{"issue":{"skip_drafts":true}}}]}`:                "o/r auto_rules",
		`{"auto_rules":{"issue":{"allow":["comment","label"],"prompt":"Fix #{{.Number}}"}}}`:         "",
		`{"auto_rules":{"issue":{"allow":["push"]}}}`:                                                "unknown action",
		`{"auto_rules":{"issue":{"prompt":"{{.Number"}}}`:                                            "prompt:",
		`{"auto_rules":{"pull_request:checks_failed":{"allow":["update_pr"]},"issue:commented":{}}}`: "",
		`{"auto_rules":{"issue:checks_failed":{}}}`:                                                  "has no trigger",
		`{"auto_rules":{"issue":{},"issue:opened":{}}}`:                                              "same key",
		`{"prompts":{"pull_request:conflict":"{{.Number"}}`:                                          "prompts:",
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
	cfg.AutoRules = map[RuleKey]Rule{"issue": {}}
	if _, ok := cfg.RulesFor(r)["issue"]; !ok {
		t.Fatal("repo without rules should inherit the global ones")
	}
	r.AutoRules = map[RuleKey]Rule{"pull_request": {}}
	if _, ok := cfg.RulesFor(r)["issue"]; ok {
		t.Fatal("repo rules should replace the global ones")
	}
}

func TestLoadCanonicalizesKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"auto_rules":{"issue:opened":{"labels":["bug"]}},"prompts":{"pull_request:opened":"x"}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.AutoRules["issue"]; !ok {
		t.Fatalf("issue:opened should be stored as issue: %v", cfg.AutoRules)
	}
	if cfg.Prompts["pull_request"] != "x" {
		t.Fatalf("pull_request:opened should be stored as pull_request: %v", cfg.Prompts)
	}
}

func TestActionToolsScope(t *testing.T) {
	tools := ActionComment.Tools("o/r", 12)
	if tools[len(tools)-1] != "Bash(gh api --method POST repos/o/r/pulls/12/comments:*)" {
		t.Fatalf("thread replies should be scoped to the session's PR: %v", tools)
	}
}
