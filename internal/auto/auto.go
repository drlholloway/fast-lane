// Package auto decides which new events start Claude sessions on their own.
package auto

import (
	"slices"
	"strings"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// Match reports whether ev should start a session without asking on repo r.
// The repo must have auto-run on. With no rules configured every event
// matches; otherwise only kinds that have a rule do, and only when the rule
// accepts the event.
func Match(cfg *config.Config, r config.Repo, ev github.Event) bool {
	if !r.AutoRun {
		return false
	}
	rules := cfg.RulesFor(r)
	if rules == nil {
		return true
	}
	rule, ok := rules[ev.Kind]
	return ok && Accepts(rule, ev)
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
