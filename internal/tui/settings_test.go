package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/doctor"
)

func settingIndex(t *testing.T, label string) int {
	t.Helper()
	for i, s := range settings {
		if s.label == label {
			return i
		}
	}
	t.Fatalf("no setting %q", label)
	return -1
}

func openSetting(t *testing.T, m *Model, label string) {
	t.Helper()
	keys(m, "5")
	m.setCursor = settingIndex(t, label)
	keys(m, "enter")
}

func TestSettingsPollInterval(t *testing.T) {
	m, _ := newTestModel(t)
	openSetting(t, m, "Poll interval")
	d := m.dialog.(*editDialog)
	d.ti.SetValue("10s")
	keys(m, "enter")
	if m.dialog == nil || !strings.Contains(d.err, "at least 30s") {
		t.Fatalf("too short an interval should be refused, err %q", d.err)
	}
	d.ti.SetValue("90s")
	keys(m, "enter")
	if m.dialog != nil || m.cfg.PollInterval.Duration != 90*time.Second {
		t.Fatalf("interval not saved: %v", m.cfg.PollInterval)
	}
	b, _ := os.ReadFile(m.cfg.Path())
	if !strings.Contains(string(b), `"poll_interval": "1m30s"`) {
		t.Fatalf("not written to the config file:\n%s", b)
	}
}

func TestSettingsCodeFoldersRescan(t *testing.T) {
	m, _ := newTestModel(t)
	code := t.TempDir()
	os.MkdirAll(filepath.Join(code, "app", ".git"), 0o755)
	os.WriteFile(filepath.Join(code, "app", ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:me/app.git\n"), 0o644)
	openSetting(t, m, "Code folders")
	d := m.dialog.(*editDialog)
	d.ta.SetValue(code + "\n\n  ")
	keys(m, "ctrl+s")
	if len(m.cfg.CodeDirs) != 1 {
		t.Fatalf("blank lines should be dropped: %q", m.cfg.CodeDirs)
	}
	if co, ok := m.mgr.CheckoutFor("me/app"); !ok || co.Dir != filepath.Join(code, "app") {
		t.Fatalf("saving should rescan: %+v %v", co, ok)
	}
	if !strings.Contains(m.status, "Found 1 checkout") {
		t.Fatalf("status %q", m.status)
	}
}

func TestSettingsCyclesAndLists(t *testing.T) {
	m, _ := newTestModel(t)
	openSetting(t, m, "Permission mode")
	if m.cfg.Claude.PermissionMode != "acceptEdits" {
		t.Fatalf("enter should cycle the mode: %q", m.cfg.Claude.PermissionMode)
	}
	m.setCursor = settingIndex(t, "Effort")
	keys(m, "enter", "enter")
	if m.cfg.Claude.Effort != "medium" || !strings.Contains(m.View(), "medium") {
		t.Fatalf("enter should cycle the effort: %q", m.cfg.Claude.Effort)
	}
	m.setCursor = settingIndex(t, "Inbox")
	keys(m, "enter")
	if !m.cfg.Inbox.Enabled || m.cfg.Inbox.AutoRun {
		t.Fatal("enter should cycle the inbox to list only")
	}

	m.setCursor = settingIndex(t, "Allowed tools")
	keys(m, "enter")
	d := m.dialog.(*editDialog)
	if !strings.Contains(d.ta.Value(), "Bash(git log:*)\n") {
		t.Fatalf("allowed tools should be listed one per line: %q", d.ta.Value())
	}
	d.ta.SetValue("Read\nBash(make test:*)")
	keys(m, "ctrl+s")
	if strings.Join(m.cfg.Claude.AllowedTools, "|") != "Read|Bash(make test:*)" {
		t.Fatalf("got %q", m.cfg.Claude.AllowedTools)
	}

	m.setCursor = settingIndex(t, "Max budget per session")
	keys(m, "enter")
	m.dialog.(*editDialog).ti.SetValue("$2.50")
	keys(m, "enter")
	if m.cfg.Claude.MaxBudgetUSD != 2.5 || !strings.Contains(m.View(), "$2.50") {
		t.Fatalf("budget %v", m.cfg.Claude.MaxBudgetUSD)
	}
}

func TestSettingsPrompts(t *testing.T) {
	m, _ := newTestModel(t)
	openSetting(t, m, "Prompts")
	p := m.dialog.(*promptsDialog)
	for i, k := range p.keys {
		if k == "issue:commented" {
			p.cursor = i
		}
	}
	keys(m, "enter")
	e := m.dialog.(*editDialog)
	if !strings.Contains(e.ta.Value(), "commented on issue") {
		t.Fatalf("editor should start from the built-in prompt: %q", e.ta.Value())
	}
	keys(m, "ctrl+s")
	if m.dialog != p || m.cfg.Prompts != nil {
		t.Fatalf("saving the built-in unchanged should store nothing and return to the list: %v", m.cfg.Prompts)
	}

	keys(m, "enter")
	e = m.dialog.(*editDialog)
	e.ta.SetValue("{{.Nope")
	keys(m, "ctrl+s")
	if e.err == "" {
		t.Fatal("a broken template should be refused")
	}
	e.ta.SetValue("Reply to {{.Actor}}")
	keys(m, "ctrl+s")
	if m.cfg.Prompts["issue:commented"] != "Reply to {{.Actor}}" || !strings.Contains(m.View(), "custom") {
		t.Fatalf("custom prompt not saved: %v", m.cfg.Prompts)
	}
	keys(m, "r")
	if _, ok := m.cfg.Prompts["issue:commented"]; ok {
		t.Fatal("r should reset to the built-in prompt")
	}
}

func TestSettingsShowsChecks(t *testing.T) {
	m, _ := newTestModel(t)
	m.Check = func(context.Context, doctor.Env) []doctor.Check {
		return []doctor.Check{
			{Name: "GitHub CLI", Detail: "gh version 2"},
			{Name: "Claude Code", Status: doctor.Fail, Detail: "claude is not on your PATH", Fix: "Install it."},
		}
	}
	run(m, m.runChecks())
	if !m.statusErr || !strings.Contains(m.status, "Setup needs attention (Claude Code)") {
		t.Fatalf("the first check should flag problems in the status line: %q", m.status)
	}
	keys(m, "5")
	v := m.View()
	for _, want := range []string{"✓ GitHub CLI", "✗ Claude Code", "Install it.", "Poll interval", "2m0s"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	m.status = ""
	run(m, m.runChecks())
	if m.status != "" {
		t.Fatal("later checks should not repeat the status warning")
	}
}

func TestManagerGetsConfigCopies(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.Repos = append(m.cfg.Repos, config.Repo{FullName: "me/app", LocalPath: "/src/app"})
	if _, ok := m.mgr.CheckoutFor("me/app"); ok {
		t.Fatal("the manager should not see unsaved changes")
	}
	run(m, m.saveConfig("saved"))
	if co, ok := m.mgr.CheckoutFor("me/app"); !ok || co.Dir != "/src/app" {
		t.Fatalf("saving should hand the manager the new config: %+v", co)
	}
}
