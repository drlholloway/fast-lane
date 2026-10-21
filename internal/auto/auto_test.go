package auto

import (
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestMatchWithoutRulesRunsEverything(t *testing.T) {
	cfg := config.Default()
	r := config.Repo{FullName: "o/r", AutoRun: true}
	if !Match(cfg, r, github.Event{Kind: config.KindIssue}) {
		t.Fatal("auto-run with no rules should match every event")
	}
	r.AutoRun = false
	if Match(cfg, r, github.Event{Kind: config.KindIssue}) {
		t.Fatal("auto-run off must never match")
	}
}

func TestMatchOnlyRuledKinds(t *testing.T) {
	cfg := config.Default()
	cfg.AutoRules = map[config.EventKind]config.Rule{config.KindDependabot: {}}
	r := config.Repo{FullName: "o/r", AutoRun: true}
	if !Match(cfg, r, github.Event{Kind: config.KindDependabot}) {
		t.Fatal("kind with an empty rule should match")
	}
	if Match(cfg, r, github.Event{Kind: config.KindPullRequest}) {
		t.Fatal("kind without a rule should not match once rules exist")
	}

	r.AutoRules = map[config.EventKind]config.Rule{config.KindPullRequest: {}}
	if Match(cfg, r, github.Event{Kind: config.KindDependabot}) || !Match(cfg, r, github.Event{Kind: config.KindPullRequest}) {
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
