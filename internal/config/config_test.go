package config

import (
	"path/filepath"
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
