package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/doctor"
	"github.com/drlholloway/fast-lane/internal/github"
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
	m.cfg.Inbox.AutoRun = true
	keys(m, "enter")
	if m.dialog == nil || m.cfg.Inbox.FullAuto {
		t.Fatal("enter on the inbox should ask before full auto")
	}
	keys(m, "y")
	if !m.cfg.Inbox.FullAuto || !strings.Contains(m.View(), "full auto") {
		t.Fatal("y should put the inbox in full auto")
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

func TestSettingsAbout(t *testing.T) {
	m, _ := newTestModel(t)
	var opened []string
	m.Browse = func(u string) error { opened = append(opened, u); return nil }
	keys(m, "5")
	v := m.View()
	for _, want := range []string{"About", "Version", "buymeacoffee.com/drlholloway", "github.com/drlholloway/fast-lane"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	m.setCursor = settingIndex(t, "Buy me a coffee")
	keys(m, "enter")
	m.setCursor = settingIndex(t, "Source")
	keys(m, "enter")
	if len(opened) != 2 || opened[0] != "https://buymeacoffee.com/drlholloway" || opened[1] != "https://github.com/drlholloway/fast-lane" {
		t.Fatalf("enter should open the links: %v", opened)
	}
	m.setCursor = settingIndex(t, "Version")
	keys(m, "enter")
	if len(opened) != 3 || opened[2] != "https://github.com/drlholloway/fast-lane/releases" {
		t.Fatalf("enter on Version should open the releases: %v", opened)
	}
}

func TestNewRelease(t *testing.T) {
	m, gh := newTestModel(t)
	var opened []string
	m.Browse = func(u string) error { opened = append(opened, u); return nil }
	m.Outdated = func(tag string, _ time.Time) bool { return tag == "v9.0.0" }
	keys(m, "5")

	gh.release = github.Release{Tag: "v0.2.0", URL: "https://example.com/v0.2.0"}
	run(m, m.checkRelease())
	if v := m.View(); strings.Contains(v, "is out") || !strings.Contains(v, "up to date (newest is v0.2.0)") {
		t.Fatalf("an older release should read as up to date:\n%s", v)
	}

	gh.release = github.Release{Tag: "v9.0.0", URL: "https://example.com/v9.0.0"}
	keys(m, "c") // checks again
	run(m, m.checkRelease())
	v := m.View()
	if !strings.Contains(v, "⬆ v9.0.0 is out") || !strings.Contains(v, "Fast Lane v9.0.0 is out (you have") {
		t.Fatalf("a newer release should show on the Version setting and in the footer:\n%s", v)
	}
	m.setCursor = settingIndex(t, "Version")
	keys(m, "enter")
	if len(opened) != 1 || opened[0] != "https://example.com/v9.0.0" {
		t.Fatalf("enter should open the new release: %v", opened)
	}

	// A lasting warning in the status line doesn't hide the notice.
	m.setStatus(true, "Setup needs attention")
	if v := m.View(); !strings.Contains(v, "Setup needs attention") || !strings.Contains(v, "Fast Lane v9.0.0 is out") {
		t.Fatalf("the status and the notice should both show:\n%s", v)
	}
}

func TestDialogsSurviveResize(t *testing.T) {
	m, _ := newTestModel(t)
	resize := func() { m.Update(tea.WindowSizeMsg{Width: 100, Height: 28}) }
	keys(m, "4", "n")
	resize() // rule form, prompt editor closed
	d := m.dialog.(*ruleDialog)
	d.cursor = len(d.fields()) - 1
	keys(m, "enter")
	resize() // prompt editor open
	m.dialog = nil
	openSetting(t, m, "Poll interval")
	resize() // single-line editor
	m.dialog = nil
	openSetting(t, m, "Allowed tools")
	resize() // multi-line editor
	if !strings.Contains(m.View(), "Allowed tools") {
		t.Fatal("dialog lost after resize")
	}
}
