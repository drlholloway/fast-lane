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
		out, err := Render(cfg, github.Event{Kind: k, Repo: "o/r", Number: 3, Title: "T", Body: "B"}, "")
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
	cfg.Prompts = map[config.EventKind]string{config.KindIssue: "fix {{.Repo}}#{{.Number}}"}
	out, err := Render(cfg, github.Event{Kind: config.KindIssue, Repo: "o/r", Number: 9}, "")
	if err != nil || out != "fix o/r#9" {
		t.Fatalf("got %q, %v", out, err)
	}
	out, err = Render(cfg, github.Event{Kind: config.KindIssue, Repo: "o/r", Number: 9}, "rule {{.Number}}")
	if err != nil || out != "rule 9" {
		t.Fatalf("rule prompt should win over cfg.Prompts, got %q, %v", out, err)
	}
	cfg.Prompts[config.KindIssue] = "{{.Nope"
	if _, err := Render(cfg, github.Event{Kind: config.KindIssue}, ""); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSystemListsAllowedActions(t *testing.T) {
	none := System(nil)
	if !strings.Contains(none, "Ask the user with AskUserQuestion before you push new branches, open pull requests,") ||
		strings.Contains(none, "You may") {
		t.Fatalf("with nothing allowed every action should need approval:\n%s", none)
	}

	some := System([]config.Action{config.ActionEdit, config.ActionPushBranch, config.ActionOpenPR})
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
