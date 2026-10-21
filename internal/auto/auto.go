// Package auto decides which new events start Claude sessions on their own.
package auto

import (
	"slices"
	"strings"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// Match reports whether ev should start a session without asking on repo r:
// r has auto-run on and RuleFor finds a rule for ev.
func Match(cfg *config.Config, r config.Repo, ev github.Event) bool {
	if !r.AutoRun {
		return false
	}
	_, ok := RuleFor(cfg, r, ev)
	return ok
}

// RuleFor returns the rule that governs ev on repo r, whether or not r has
// auto-run on. With no rules configured every event gets the zero rule;
// otherwise ok is false unless ev's kind has a rule that accepts it.
func RuleFor(cfg *config.Config, r config.Repo, ev github.Event) (config.Rule, bool) {
	rules := cfg.RulesFor(r)
	if rules == nil {
		return config.Rule{}, true
	}
	rule, ok := rules[ev.Kind]
	if !ok || !Accepts(rule, ev) {
		return config.Rule{}, false
	}
	return rule, true
}

// Accepts reports whether ev passes every filter set on rule.
func Accepts(rule config.Rule, ev github.Event) bool {
	switch {
	case rule.MinSeverity != "" && config.SeverityRank(ev.Severity) < config.SeverityRank(rule.MinSeverity):
		return false
	case len(rule.Labels) > 0 && !anyIn(ev.Labels, rule.Labels):
		return false
	case anyIn(ev.Labels, rule.SkipLabels):
		return false
	case len(rule.Authors) > 0 && !anyIn([]string{ev.Author}, rule.Authors):
		return false
	case anyIn([]string{ev.Author}, rule.SkipAuthors):
		return false
	case rule.SkipDrafts && ev.Draft:
		return false
	case rule.SkipBots && ev.Bot:
		return false
	}
	return true
}

// anyIn reports whether any of have appears in want, ignoring case.
func anyIn(have, want []string) bool {
	return slices.ContainsFunc(have, func(h string) bool {
		return slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(h, w) })
	})
}
