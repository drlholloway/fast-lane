package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestRulesAddRuleFromForm(t *testing.T) {
	m, _ := newTestModel(t)
	keys(m, "4", "n")
	d, ok := m.dialog.(*ruleDialog)
	if !ok {
		t.Fatal("n should open the rule form")
	}
	// Kind stays pull_request; trigger moves from opened to checks_failed.
	keys(m, "down", "right", "right")
	if d.trigger != config.TriggerChecksFailed {
		t.Fatalf("trigger = %s", d.trigger)
	}
	keys(m, "down", "down", "down") // labels, skip labels, authors
	typeText(m, "drlholloway, amy")
	keys(m, "down", "down", "down", "down") // skip authors, skip drafts, skip bots, allow
	keys(m, " ", "right", "right", "right", " ")
	keys(m, "ctrl+s")
	if m.dialog != nil {
		t.Fatalf("save should close the form, error %q", d.err)
	}

	rs := m.cfg.AutoRules["pull_request:checks_failed"]
	if len(rs) != 1 || strings.Join(rs[0].Authors, ",") != "drlholloway,amy" ||
		len(rs[0].Allow) != 2 || rs[0].Allow[1] != config.ActionUpdatePR {
		t.Fatalf("rule not built from the form: %+v", rs)
	}
	b, _ := os.ReadFile(m.cfg.Path())
	if !strings.Contains(string(b), `"pull_request:checks_failed"`) {
		t.Fatalf("rule should be saved to the config file:\n%s", b)
	}
	if v := m.View(); !strings.Contains(v, "pull_request:checks_failed") || !strings.Contains(v, "authors drlholloway,amy") {
		t.Fatalf("rules tab should list the rule:\n%s", v)
	}
}

func TestRulesFormHidesFieldsByKind(t *testing.T) {
	m, _ := newTestModel(t)
	keys(m, "4", "n")
	d := m.dialog.(*ruleDialog)
	d.inputs[fLabels].SetValue("bug")
	keys(m, "right", "right") // pull_request -> issue -> dependabot_alert
	if d.kind != config.KindDependabot {
		t.Fatalf("kind = %s", d.kind)
	}
	keys(m, "down", "down", "right", "right", "right") // severity: any -> low -> medium -> high
	keys(m, "ctrl+s")
	rs := m.cfg.AutoRules["dependabot_alert"]
	if len(rs) != 1 || rs[0].MinSeverity != "high" || rs[0].Labels != nil {
		t.Fatalf("hidden fields should be dropped: %+v", rs)
	}
}

func TestRulesFormRejectsInvalidPrompt(t *testing.T) {
	m, _ := newTestModel(t)
	keys(m, "4", "n")
	d := m.dialog.(*ruleDialog)
	d.prompt = "{{.Number"
	keys(m, "ctrl+s")
	if m.dialog == nil || !strings.Contains(d.err, "prompt") || m.cfg.AutoRules != nil {
		t.Fatalf("an invalid template must not save: err %q, rules %v", d.err, m.cfg.AutoRules)
	}
}

func TestRulesPromptEditor(t *testing.T) {
	m, _ := newTestModel(t)
	keys(m, "4", "n")
	d := m.dialog.(*ruleDialog)
	d.cursor = len(d.fields()) - 1 // prompt
	keys(m, "enter")
	if !d.editing || !strings.Contains(d.ta.Value(), "Review pull request #{{.Number}}") {
		t.Fatalf("the editor should start from the default template: %q", d.ta.Value())
	}
	keys(m, "ctrl+s")
	if d.editing || d.prompt != "" {
		t.Fatalf("keeping the default unchanged should store no custom prompt: %q", d.prompt)
	}
	keys(m, "enter")
	d.ta.SetValue("Just say hi to #{{.Number}}")
	keys(m, "ctrl+s", "ctrl+s")
	if rs := m.cfg.AutoRules["pull_request"]; len(rs) != 1 || rs[0].Prompt != "Just say hi to #{{.Number}}" {
		t.Fatalf("custom prompt not saved: %+v", rs)
	}
}

func TestRulesEditReorderDelete(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{
		"pull_request": {{Authors: []string{"dependabot[bot]"}}, {SkipDrafts: true}},
		"issue":        {{}},
	}
	keys(m, "4")
	// Rows: pull_request #1, pull_request #2, issue #1.
	keys(m, "j", "K")
	if rs := m.cfg.AutoRules["pull_request"]; !rs[0].SkipDrafts || m.ruleCursor != 0 {
		t.Fatalf("K should move the rule up and follow it: %+v cursor %d", rs, m.ruleCursor)
	}

	// Edit the first rule: switching its trigger moves it to another key.
	keys(m, "enter", "down", "right", "ctrl+s")
	if len(m.cfg.AutoRules["pull_request"]) != 1 || len(m.cfg.AutoRules["pull_request:changes_requested"]) != 1 {
		t.Fatalf("editing the trigger should move the rule: %v", m.cfg.AutoRules)
	}

	rows := m.ruleRows()
	for i, r := range rows {
		if r.key == "issue" {
			m.ruleCursor = i
		}
	}
	keys(m, "d", "y")
	if _, ok := m.cfg.AutoRules["issue"]; ok {
		t.Fatalf("d then y should delete the rule: %v", m.cfg.AutoRules)
	}
}

func TestRulesRepoScope(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{"pull_request": {{}}}
	m.cfg.Watch("me/app")
	keys(m, "4", "]")
	if m.scope() != "me/app" || !m.inherited("me/app") {
		t.Fatalf("] should switch to the repo, which inherits: %q", m.scope())
	}
	if v := m.View(); !strings.Contains(v, "Uses the global rules") {
		t.Fatalf("inherited scope should say so:\n%s", v)
	}
	keys(m, "d")
	if m.dialog != nil {
		t.Fatal("inherited rules cannot be deleted from the repo scope")
	}

	keys(m, "c")
	own := m.cfg.Repo("me/app").AutoRules
	if len(own["pull_request"]) != 1 {
		t.Fatalf("c should copy the global rules: %v", own)
	}
	own["pull_request"][0].SkipBots = true
	if m.cfg.AutoRules["pull_request"][0].SkipBots {
		t.Fatal("the copy must not share memory with the global rules")
	}

	keys(m, "a")
	if !m.cfg.Repo("me/app").AutoRun {
		t.Fatal("a should toggle the repo's auto-run")
	}
	keys(m, "d", "y")
	if m.cfg.Repo("me/app").AutoRules != nil || !m.inherited("me/app") {
		t.Fatal("deleting the last repo rule should fall back to the global rules")
	}
}

func TestRulesInboxCycle(t *testing.T) {
	m, _ := newTestModel(t)
	keys(m, "4", "i")
	if !m.cfg.Inbox.Enabled || m.cfg.Inbox.AutoRun {
		t.Fatal("first i: list only")
	}
	keys(m, "i")
	if m.cfg.Inbox.Enabled {
		t.Fatal("second i: off")
	}
	keys(m, "i")
	if !m.cfg.Inbox.Enabled || !m.cfg.Inbox.AutoRun {
		t.Fatal("third i: back on with auto-run")
	}
}

func TestReposJumpToRules(t *testing.T) {
	m, _ := newTestModel(t)
	run(m, m.load())
	m.cfg.Watch("me/app")
	keys(m, "3", "r")
	if m.tab != tabRules || m.scope() != "me/app" {
		t.Fatalf("r should open the repo's rules: tab %d scope %q", m.tab, m.scope())
	}
}

func TestRulesModel(t *testing.T) {
	m, gh := newTestModel(t)
	keys(m, "4", "n")
	d := m.dialog.(*ruleDialog)
	d.cursor = slices.Index(d.fields(), fModel)
	keys(m, "down", "up") // focus the model field
	typeText(m, "claude-opus-5-5")
	if !strings.Contains(m.View(), "claude-sonnet-5-5") {
		t.Fatalf("the model field should suggest model IDs:\n%s", m.View())
	}
	keys(m, "ctrl+s")
	rs := m.cfg.AutoRules["pull_request"]
	if len(rs) != 1 || rs[0].Model != "claude-opus-5-5" || !strings.Contains(m.View(), "claude-opus-5-5") {
		t.Fatalf("model not saved or shown: %+v", rs)
	}

	// Auto-run and manual starts both use the rule's model.
	m.cfg.Watch("me/app").AutoRun = true
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	gh.events = []github.Event{ev(2, config.KindPullRequest), ev(3, config.KindPullRequest)}
	m.cfg.Repo("me/app").AutoRun = false
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	m.cfg.Repo("me/app").AutoRun = true
	gh.events = append(gh.events, ev(4, config.KindPullRequest))
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if ss := m.mgr.Snapshot(); len(ss) != 1 || ss[0].Model != "claude-opus-5-5" {
		t.Fatalf("auto-run should use the rule's model: %+v", ss)
	}
	keys(m, "1")
	for i, e := range m.feed {
		if e.Number == 2 {
			m.eventCursor = i
		}
	}
	keys(m, "enter")
	if !strings.Contains(m.View(), "Model: claude-opus-5-5") {
		t.Fatalf("the start dialog should show the model:\n%s", m.View())
	}
}
