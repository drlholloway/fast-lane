package prompt

import (
	"strings"
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestRenderDefaults(t *testing.T) {
	cfg := config.Default()
	for _, k := range config.AllKinds {
		out, err := Render(cfg, github.Event{Kind: k, Repo: "o/r", Number: 3, Title: "T", Body: "B"}, github.Change{}, "")
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if !strings.Contains(out, "o/r") || !strings.Contains(out, "#3") {
			t.Fatalf("%s: missing fields in %q", k, out)
		}
	}
}

func TestRenderOverride(t *testing.T) {
	cfg := config.Default()
	cfg.Prompts = map[config.RuleKey]string{"issue": "fix {{.Repo}}#{{.Number}}"}
	out, err := Render(cfg, github.Event{Kind: config.KindIssue, Repo: "o/r", Number: 9}, github.Change{}, "")
	if err != nil || out != "fix o/r#9" {
		t.Fatalf("got %q, %v", out, err)
	}
	out, err = Render(cfg, github.Event{Kind: config.KindIssue, Repo: "o/r", Number: 9}, github.Change{}, "rule {{.Number}}")
	if err != nil || out != "rule 9" {
		t.Fatalf("rule prompt should win over cfg.Prompts, got %q, %v", out, err)
	}
	cfg.Prompts["issue"] = "{{.Nope"
	if _, err := Render(cfg, github.Event{Kind: config.KindIssue}, github.Change{}, ""); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSystemListsAllowedActions(t *testing.T) {
	none := System(nil, false, false)
	if !strings.Contains(none, "Ask the user with AskUserQuestion before you push new branches, open pull requests,") ||
		strings.Contains(none, "You may") {
		t.Fatalf("with nothing allowed every action should need approval:\n%s", none)
	}

	some := System([]config.Action{config.ActionEdit, config.ActionPushBranch, config.ActionOpenPR}, false, false)
	if !strings.Contains(some, "- You may push new branches and open pull requests without asking.") {
		t.Fatalf("allowed actions missing:\n%s", some)
	}
	if strings.Contains(some, "before you push new branches") || !strings.Contains(some, ", comment on issues") {
		t.Fatalf("allowed actions should leave the ask list:\n%s", some)
	}
	if !strings.Contains(some, "Always ask before you push to the default branch") {
		t.Fatalf("default-branch guard must always be present:\n%s", some)
	}
}

func TestRenderTriggers(t *testing.T) {
	cfg := config.Default()
	ev := github.Event{Kind: config.KindPullRequest, Repo: "o/r", Number: 5, Title: "T", HeadSHA: "abc"}

	out, err := Render(cfg, ev, github.Change{Trigger: config.TriggerChangesRequested, Actor: "bob", Detail: "rename x"}, "")
	if err != nil || !strings.Contains(out, "@bob requested changes") || !strings.Contains(out, "rename x") {
		t.Fatalf("trigger template should use actor and detail: %q, %v", out, err)
	}

	out, err = Render(cfg, ev, github.Change{Trigger: config.TriggerLabeled, Detail: "bug"}, "")
	if err != nil || !strings.HasPrefix(out, "Triggered by: labeled (bug)\n\nReview pull request #5") {
		t.Fatalf("a trigger without a template should reuse the kind's and say why: %q, %v", out, err)
	}

	cfg.Prompts = map[config.RuleKey]string{"pull_request:checks_failed": "fix CI on {{.HeadSHA}} ({{.Trigger}})"}
	out, err = Render(cfg, ev, github.Change{Trigger: config.TriggerChecksFailed}, "")
	if err != nil || out != "fix CI on abc (checks_failed)" {
		t.Fatalf("configured trigger prompt should win over the default: %q, %v", out, err)
	}
}

func TestSystemWorktreeNote(t *testing.T) {
	if strings.Contains(System(nil, false, false), "worktree") {
		t.Fatal("clones should not mention worktrees")
	}
	if !strings.Contains(System(nil, true, false), "gh pr checkout N --branch fast-lane/pr-N") {
		t.Fatal("worktree sessions should learn how to avoid branch clashes")
	}
}

func TestSystemFullAuto(t *testing.T) {
	s := System([]config.Action{config.ActionComment}, true, true)
	for _, want := range []string{"full auto mode", "Merge only when checks pass", "Don't ask the user", "Only a human may delete or transfer the repository", "git worktree of the user's own checkout"} {
		if !strings.Contains(s, want) {
			t.Errorf("full auto system prompt missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Ask the user with AskUserQuestion") {
		t.Fatal("full auto must not tell Claude to ask")
	}
}
