package auto

import (
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestRuleForWithoutRules(t *testing.T) {
	cfg := config.Default()
	r := config.Repo{FullName: "o/r"}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindIssue}, config.TriggerOpened); !ok {
		t.Fatal("with no rules every opened item should match")
	}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindIssue}, config.TriggerCommented); ok {
		t.Fatal("with no rules other triggers should not match")
	}
}

func TestRuleForOnlyRuledKeys(t *testing.T) {
	cfg := config.Default()
	cfg.AutoRules = map[config.RuleKey]config.Rules{
		"dependabot_alert":           {{}},
		"pull_request:checks_failed": {{Allow: []config.Action{config.ActionUpdatePR}}},
	}
	r := config.Repo{FullName: "o/r"}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindDependabot}, config.TriggerOpened); !ok {
		t.Fatal("kind with an empty rule should match")
	}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindPullRequest}, config.TriggerOpened); ok {
		t.Fatal("opened PRs have no rule, so they should not match")
	}
	rule, ok := RuleFor(cfg, r, github.Event{Kind: config.KindPullRequest}, config.TriggerChecksFailed)
	if !ok || len(rule.Allow) != 1 {
		t.Fatalf("trigger rule should match and be returned: %+v %v", rule, ok)
	}

	r.AutoRules = map[config.RuleKey]config.Rules{"pull_request": {{}}}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindDependabot}, config.TriggerOpened); ok {
		t.Fatal("repo rules should replace the global rules")
	}
}

func TestAccepts(t *testing.T) {
	tests := []struct {
		name string
		rule config.Rule
		ev   github.Event
		want bool
	}{
		{"empty rule", config.Rule{}, github.Event{}, true},
		{"severity at minimum", config.Rule{MinSeverity: "high"}, github.Event{Severity: "high"}, true},
		{"severity above", config.Rule{MinSeverity: "high"}, github.Event{Severity: "critical"}, true},
		{"severity below", config.Rule{MinSeverity: "high"}, github.Event{Severity: "medium"}, false},
		{"code scanning level", config.Rule{MinSeverity: "medium"}, github.Event{Severity: "error"}, true},
		{"missing severity", config.Rule{MinSeverity: "low"}, github.Event{}, false},
		{"has label", config.Rule{Labels: []string{"bug", "security"}}, github.Event{Labels: []string{"Bug"}}, true},
		{"lacks label", config.Rule{Labels: []string{"bug"}}, github.Event{Labels: []string{"docs"}}, false},
		{"skip label", config.Rule{SkipLabels: []string{"wontfix"}}, github.Event{Labels: []string{"bug", "wontfix"}}, false},
		{"allowed author", config.Rule{Authors: []string{"Amy"}}, github.Event{Author: "amy"}, true},
		{"other author", config.Rule{Authors: []string{"amy"}}, github.Event{Author: "bob"}, false},
		{"skip author", config.Rule{SkipAuthors: []string{"bob"}}, github.Event{Author: "bob"}, false},
		{"skip draft", config.Rule{SkipDrafts: true}, github.Event{Draft: true}, false},
		{"ready PR", config.Rule{SkipDrafts: true}, github.Event{}, true},
		{"skip bot", config.Rule{SkipBots: true}, github.Event{Author: "renovate[bot]", Bot: true}, false},
	}
	for _, tt := range tests {
		if got := Accepts(tt.rule, tt.ev); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRuleForFirstMatchWins(t *testing.T) {
	cfg := config.Default()
	cfg.AutoRules = map[config.RuleKey]config.Rules{"pull_request": {
		{Authors: []string{"dependabot[bot]"}, Allow: []config.Action{config.ActionMerge}},
		{SkipAuthors: []string{"me"}},
	}}
	r := config.Repo{FullName: "o/r"}
	rule, ok := RuleFor(cfg, r, github.Event{Kind: config.KindPullRequest, Author: "dependabot[bot]"}, config.TriggerOpened)
	if !ok || len(rule.Allow) != 1 {
		t.Fatalf("dependabot PR should get the first rule: %+v %v", rule, ok)
	}
	rule, ok = RuleFor(cfg, r, github.Event{Kind: config.KindPullRequest, Author: "amy"}, config.TriggerOpened)
	if !ok || len(rule.Allow) != 0 {
		t.Fatalf("other PRs should fall through to the second rule: %+v %v", rule, ok)
	}
	if _, ok := RuleFor(cfg, r, github.Event{Kind: config.KindPullRequest, Author: "me"}, config.TriggerOpened); ok {
		t.Fatal("a PR no rule accepts should not match")
	}
}

func TestDecidePersonalTriggers(t *testing.T) {
	cfg := config.Default()
	cfg.AutoRules = map[config.RuleKey]config.Rules{"pull_request": {{}}}
	off := config.Repo{FullName: "o/r"} // auto_run off, or not watched at all
	pr := github.Event{Kind: config.KindPullRequest}

	if _, ok := Decide(cfg, off, pr, config.TriggerOpened); ok {
		t.Fatal("repo triggers need the repo's auto_run")
	}
	if rule, ok := Decide(cfg, off, pr, config.TriggerReviewRequested); !ok || len(rule.Allow) != 0 {
		t.Fatal("review requests should auto-run with nothing pre-approved by default")
	}

	cfg.AutoRules["pull_request:review_requested"] = config.Rules{{SkipBots: true, Allow: []config.Action{config.ActionReview}}}
	if rule, ok := Decide(cfg, off, pr, config.TriggerReviewRequested); !ok || len(rule.Allow) != 1 {
		t.Fatal("a rule should opt in to submitting reviews")
	}
	if _, ok := Decide(cfg, off, github.Event{Kind: config.KindPullRequest, Bot: true}, config.TriggerReviewRequested); ok {
		t.Fatal("a configured rule that rejects the item should stop it")
	}

	cfg.Inbox.AutoRun = false
	if _, ok := Decide(cfg, off, pr, config.TriggerReviewRequested); ok {
		t.Fatal("inbox auto_run off should stop personal triggers")
	}
}
