package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/config"
)

// Full auto lets Claude act on its own judgment: approve, request changes,
// merge, push, close. Only deleting or transferring a repository and
// destroying its history are refused; see internal/guard.

const fullAutoWarning = "In full auto, every trigger runs and Claude acts on its own judgment without asking: it approves " +
	"or requests changes, merges, pushes, comments and closes. It never asks you a question.\n\n" +
	"Only a human may delete or transfer the repository, force-push or delete its default branch, delete or move tags " +
	"and releases, rewrite history, or change rulesets. Fast Lane refuses those commands."

type protectedMsg struct {
	repo  string
	added []string
	err   error
}

// cycleAuto moves repo through off → auto-run → full auto → off. Entering
// full auto asks first, then protects the repo's history on GitHub.
func (m *Model) cycleAuto(repo string) tea.Cmd {
	rc := m.cfg.Repo(repo)
	switch {
	case rc.FullAuto:
		rc.AutoRun, rc.FullAuto = false, false
		return m.saveConfig("Auto-run off for %s", repo)
	case !rc.AutoRun:
		rc.AutoRun = true
		if m.cfg.RulesFor(*rc) != nil {
			return m.saveConfig("Auto-run on for %s: new events matching your auto rules start Claude sessions without asking", repo)
		}
		return m.saveConfig("Auto-run on for %s: new events start Claude sessions without asking", repo)
	}
	msg := sBad.Render("Turn on full auto for "+repo+"?") + "\n\n" + fullAutoWarning + "\n\n" +
		"Fast Lane will also add GitHub rulesets to " + repo + " that stop anyone from force-pushing or deleting its " +
		"default branch, or deleting or moving its tags. You can change them in the repo's settings."
	m.dialog = newConfirmDialog(msg, func(m *Model) tea.Cmd {
		rc := m.cfg.Repo(repo)
		if rc == nil {
			return nil
		}
		rc.AutoRun, rc.FullAuto = true, true
		return tea.Batch(m.saveConfig("Full auto on for %s; adding history protection on GitHub…", repo), m.protect(repo))
	}, m.width)
	return nil
}

func (m *Model) protect(repo string) tea.Cmd {
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		added, err := gh.Protect(ctx, repo)
		return protectedMsg{repo: repo, added: added, err: err}
	}
}

func (m *Model) onProtected(msg protectedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus(true, "Full auto on for %s, but its history rulesets could not be added (%v); Fast Lane's own check still refuses destructive commands",
			msg.repo, msg.err)
	case len(msg.added) == 0:
		m.setStatus(false, "Full auto on for %s; its history rulesets were already in place", msg.repo)
	default:
		m.setStatus(false, "Full auto on for %s; added rulesets: %s", msg.repo, strings.Join(msg.added, ", "))
	}
}

// cycleInbox moves the inbox through auto-run → full auto → list only →
// off → auto-run. Entering full auto asks first.
func (m *Model) cycleInbox() tea.Cmd {
	in := &m.cfg.Inbox
	switch {
	case in.FullAuto:
		in.AutoRun, in.FullAuto = false, false
		return m.saveConfig("Inbox: listing review requests, mentions and assignments without running Claude")
	case in.Enabled && in.AutoRun:
		msg := sBad.Render("Turn on full auto for the inbox?") + "\n\n" +
			"Review requests, mentions and assignments, on any repo, will run Claude in full auto. " + fullAutoWarning
		m.dialog = newConfirmDialog(msg, func(m *Model) tea.Cmd {
			m.cfg.Inbox.Enabled, m.cfg.Inbox.AutoRun, m.cfg.Inbox.FullAuto = true, true, true
			return m.saveConfig("Inbox: full auto")
		}, m.width)
		return nil
	case in.Enabled:
		in.Enabled = false
		return m.saveConfig("Inbox: off")
	default:
		in.Enabled, in.AutoRun = true, true
		return m.saveConfig("Inbox: on, running Claude on review requests, mentions and assignments")
	}
}

func inboxLabel(in config.Inbox) string {
	switch {
	case in.Enabled && in.FullAuto:
		return "full auto"
	case in.Enabled && in.AutoRun:
		return "on, auto-run"
	case in.Enabled:
		return "on, list only"
	}
	return "off"
}

// fullAutoBadge names what runs in full auto, for the header.
func (m *Model) fullAutoBadge() string {
	n := 0
	for _, r := range m.cfg.Repos {
		if r.FullAuto {
			n++
		}
	}
	var parts []string
	if n > 0 {
		parts = append(parts, count(n, "repo", "repos"))
	}
	if m.cfg.Inbox.Enabled && m.cfg.Inbox.FullAuto {
		parts = append(parts, "inbox")
	}
	if len(parts) == 0 {
		return ""
	}
	return sBad.Render(fmt.Sprintf("⚡ full auto: %s", strings.Join(parts, " + ")))
}
