// Package auto decides which new events start Claude sessions on their own.
package auto

import (
	"slices"
	"strings"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// RuleFor returns the rule that governs trigger t on ev in repo r, whether
// or not r has auto-run on. With no rules configured, newly opened items get
// the zero rule and other triggers get none; otherwise the first rule for
// the kind and trigger that accepts ev wins, and ok is false if none does.
func RuleFor(cfg *config.Config, r config.Repo, ev github.Event, t config.Trigger) (config.Rule, bool) {
	rules := cfg.RulesFor(r)
	if rules == nil {
		return config.Rule{}, t == config.TriggerOpened
	}
	for _, rule := range rules[config.Key(ev.Kind, t)] {
		if Accepts(rule, ev) {
			return rule, true
		}
	}
	return config.Rule{}, false
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
