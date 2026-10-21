package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/doctor"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/local"
	"github.com/drlholloway/fast-lane/internal/version"
)

// TestScreenshots renders each screen with made-up data, in color, for the
// wiki. It runs only when FASTLANE_SCREENSHOTS names a folder; each screen
// is written there as <name>.ansi, ready for charmbracelet/freeze:
//
//	FASTLANE_SCREENSHOTS=/tmp/shots go test ./internal/tui -run TestScreenshots
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("FASTLANE_SCREENSHOTS")
	if dir == "" {
		t.Skip("set FASTLANE_SCREENSHOTS to a folder to render screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	version.Version = "v0.1.0"
	defer func() { version.Version = "" }()
	m := demoModel(t)
	// Image fonts lack the braille spinner frames.
	m.spinner.Spinner = spinner.Spinner{Frames: []string{"●"}, FPS: time.Second}
	// shot presses ks, then saves the screen at the given height.
	shot := func(name string, height int, ks ...string) {
		t.Helper()
		keys(m, ks...)
		m.Update(tea.WindowSizeMsg{Width: m.width, Height: height})
		m.status = "" // status lines depend on timing; keep shots stable
		// The demo config lives in a temp folder; show where yours would be.
		view := strings.ReplaceAll(m.View(), filepath.Dir(m.cfg.Path()), "~/.config/fast-lane")
		// Image fonts have no emoji.
		view = strings.ReplaceAll(view, "☕ ", "")
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(view), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	shot("events", 15, "1")
	m.eventCursor = feedIndex(m, "acme/api:dependabot_alert:31")
	shot("start-session", 24, "enter")
	keys(m, "esc")

	m.sessCursor = sessionIndex(m, 3)
	m.updateTranscript()
	shot("sessions", 22, "2")

	review := m.sessions[sessionIndex(m, 4)]
	m.openRequest(review, review.Pending[0])
	shot("permission", 22)
	m.dialog = nil
	m.openRequest(review, review.Pending[1])
	shot("question", 24)
	m.dialog = nil

	shot("repos", 14, "3")
	m.width = 134 // room for every column of the rules table
	shot("rules", 15, "4")
	m.width = 118
	m.ruleCursor = 3 // pull_request:checks_failed
	keys(m, "enter")
	d := m.dialog.(*ruleDialog)
	d.cursor = len(d.fields()) - 4 // Allow
	d.allowAt = 3                  // update_pr
	shot("rule-form", 25)
	m.dialog = nil

	m.setCursor = settingIndex(t, "Effort")
	shot("settings", 37, "5")
}

func feedIndex(m *Model, key string) int {
	for i, e := range m.feed {
		if e.Key == key {
			return i
		}
	}
	return 0
}

func sessionIndex(m *Model, id int) int {
	for i, s := range m.sessions {
		if s.ID == id {
			return i
		}
	}
	return 0
}

// demoModel is Fast Lane for a made-up developer working on a made-up
// "acme" organization, partway through a day.
func demoModel(t *testing.T) *Model {
	m, gh := newTestModel(t)
	m.width, m.height = 118, 26
	// Paths under the home folder show as ~/…, so no real path appears.
	home, _ := os.UserHomeDir()
	at := func(p string) string { return filepath.Join(home, p) }
	m.layout()
	m.login = "demo-dev"
	m.loading = false
	m.Update(loadedMsg{login: "demo-dev", repos: []github.Repo{
		{FullName: "acme/api", Description: "The Acme public API", Private: true, PushedAt: time.Now()},
		{FullName: "acme/web", Description: "acme.dev and the dashboard", PushedAt: time.Now()},
		{FullName: "acme/docs-site", Description: "Developer docs"},
		{FullName: "acme/infra", Description: "Terraform and deploy scripts", Private: true},
		{FullName: "demo-dev/dotfiles", Description: "My shell setup"},
	}, starred: []github.Repo{{FullName: "charmbracelet/bubbletea", Description: "A powerful little TUI framework"}},
		myPRs: []github.Event{{Repo: "other-org/cli"}}})
	m.cfg.CodeDirs = []string{"~/Code"}
	m.mgr.SetCheckouts(map[string]local.Checkout{
		"acme/api": {Dir: at("Code/api"), Remote: "origin"},
		"acme/web": {Dir: at("Code/web"), Remote: "origin"},
	})
	for _, r := range []string{"acme/api", "acme/web", "acme/docs-site"} {
		m.cfg.Watch(r)
	}
	m.cfg.Repo("acme/api").AutoRun = true
	m.cfg.Repo("acme/web").AutoRun = true
	m.cfg.Repo("acme/docs-site").Events = []config.EventKind{config.KindPullRequest, config.KindIssue}
	m.cfg.Claude.Model, m.cfg.Claude.Effort = "claude-opus-5-5", "medium"
	m.cfg.WorkspaceDir = at(".local/share/fast-lane/workspaces")

	bot, me := []string{"dependabot[bot]"}, []string{"demo-dev"}
	fixPR := []config.Action{config.ActionEdit, config.ActionUpdatePR}
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{
		"pull_request": {
			{Authors: bot, Prompt: "Handle Dependabot pull request #{{.Number}}…", Model: "claude-sonnet-5-5", Effort: "low",
				Allow: []config.Action{config.ActionEdit, config.ActionUpdatePR, config.ActionComment, config.ActionMerge}},
			{SkipDrafts: true, SkipBots: true, SkipAuthors: me},
		},
		"pull_request:checks_failed":     {{Authors: me, Allow: fixPR}},
		"pull_request:changes_requested": {{Authors: me, Allow: append(fixPR, config.ActionComment)}},
		"pull_request:review_requested":  {{Model: "claude-opus-5-5", Effort: "high"}},
		"issue":                          {{Labels: []string{"bug"}, Allow: []config.Action{config.ActionEdit, config.ActionPushBranch, config.ActionOpenPR}}},
		"dependabot_alert":               {{MinSeverity: "high", Allow: []config.Action{config.ActionEdit, config.ActionPushBranch, config.ActionOpenPR}}},
	}
	m.mgr.SetConfig(m.cfg)

	now := time.Now()
	item := func(repo string, k config.EventKind, n int, title, author string, age time.Duration) github.Event {
		return github.Event{Key: github.EventKey(repo, k, n), Repo: repo, Kind: k, Number: n, Title: title,
			URL: "https://github.com/" + repo, Author: author, CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
	}
	ci := item("acme/api", config.KindPullRequest, 142, "Add rate limiting to /search", "demo-dev", 6*time.Minute)
	ci.HeadSHA, ci.Checks, ci.Branch = "9f3c2e1", "FAILURE", "search-rate-limit"
	vite := item("acme/web", config.KindPullRequest, 88, "Bump vite from 6.2.0 to 6.2.4", "dependabot[bot]", 40*time.Minute)
	vite.Bot = true
	alert := item("acme/api", config.KindDependabot, 31, "golang.org/x/net: HTTP/2 rapid reset can cause excessive work", "dependabot", 25*time.Minute)
	alert.Severity, alert.Bot = "critical", true
	scan := item("acme/api", config.KindCodeScanning, 7, "Incomplete URL substring sanitization", "CodeQL", 3*time.Hour)
	scan.Severity = "high"
	bug := item("acme/web", config.KindIssue, 97, "Dark mode toggle forgets its setting after reload", "sam-k", 2*time.Hour)
	bug.Labels = []string{"bug"}
	draft := item("acme/web", config.KindPullRequest, 91, "Redesign the billing page", "rivera", 5*time.Hour)
	draft.Draft = true
	typo := item("acme/docs-site", config.KindIssue, 5, "Typo in the install guide", "new-contributor", 26*time.Hour)
	review := item("other-org/cli", config.KindPullRequest, 12, "Support named config profiles", "lee-w", 12*time.Minute)

	gh.events = []github.Event{ci, vite, alert, scan, bug, draft, typo}
	m.events["acme/api"] = []github.Event{ci, alert, scan}
	m.events["acme/web"] = []github.Event{vite, bug, draft}
	m.events["acme/docs-site"] = []github.Event{typo}
	m.st.Inbox[review.Key] = review
	for _, k := range []string{ci.Key, alert.Key, bug.Key, review.Key} {
		m.fresh[k] = true
	}
	m.st.LastChange[ci.Key] = github.Change{Trigger: config.TriggerChecksFailed}
	m.st.LastChange[review.Key] = github.Change{Trigger: config.TriggerReviewRequested}
	m.rebuildFeed()

	sess := func(id int, ev github.Event, st claude.Status, cost float64, age time.Duration, model, effort string, lines ...claude.Line) *claude.Session {
		return &claude.Session{ID: id, Event: ev, Status: st, ClaudeID: "c", CostUSD: cost, Started: now.Add(-age),
			Updated: now.Add(-age / 2), Model: model, Effort: effort, Lines: lines,
			Dir:      at(fmt.Sprintf(".local/share/fast-lane/workspaces/.worktrees/%s/session-%d", ev.Repo, id)),
			Checkout: at("Code/" + ev.Repo[strings.Index(ev.Repo, "/")+1:])}
	}
	L := func(k claude.LineKind, s string) claude.Line { return claude.Line{Kind: k, Text: s} }
	m.mgr.Restore([]*claude.Session{
		sess(1, bug, claude.StatusIdle, 0.84, 2*time.Hour, "", "",
			L(claude.LineText, "Reproduced it: the toggle writes the theme to sessionStorage, which is cleared on reload."),
			L(claude.LineDone, "done · $0.84")),
		sess(2, vite, claude.StatusIdle, 0.12, 38*time.Minute, "claude-sonnet-5-5", "low",
			L(claude.LineTool, "Bash: gh pr checks 88 --watch"),
			L(claude.LineText, "All 6 checks passed."),
			L(claude.LineTool, "Bash: gh pr merge 88 --squash --delete-branch"),
			L(claude.LineText, "Merged #88: vite 6.2.0 → 6.2.4, a patch release with no breaking changes."),
			L(claude.LineDone, "done · $0.12")),
		sess(3, ci, claude.StatusRunning, 0.37, 5*time.Minute, "", "",
			L(claude.LineInfo, "fetching origin in ~/Code/api"),
			L(claude.LineInfo, "creating worktree …/.worktrees/acme/api/session-3 at origin/main"),
			L(claude.LineText, "CI failed on the test job for #142. Let me read the failing logs."),
			L(claude.LineTool, "Bash: gh pr checks 142"),
			L(claude.LineTool, "Bash: gh run view 18342 --log-failed"),
			L(claude.LineText, "TestSearchRateLimit expects a 429 after 10 requests, but the limiter keys on X-Forwarded-For and the test client never sets it, so the limit is never reached. The limiter should fall back to the remote address."),
			L(claude.LineTool, "Bash: gh pr checkout 142"),
			L(claude.LineTool, "Read: internal/search/limit.go"),
			L(claude.LineTool, "Edit: internal/search/limit.go"),
			L(claude.LineTool, "Bash: go test ./internal/search/..."),
			L(claude.LineText, "All tests pass. Pushing the fix to the PR branch."),
			L(claude.LineTool, "Bash: git push origin HEAD:search-rate-limit")),
		sess(4, review, claude.StatusWaiting, 0.52, 11*time.Minute, "claude-opus-5-5", "high",
			L(claude.LineTool, "Bash: gh pr view 12 --repo other-org/cli"),
			L(claude.LineTool, "Bash: gh pr diff 12 --repo other-org/cli"),
			L(claude.LineTool, "Bash: go test ./..."),
			L(claude.LineText, "The profiles feature works and is well tested. One problem: an unknown --profile silently falls back to the default profile instead of failing.")),
	})
	m.st.EventSessions[bug.Key], m.st.EventSessions[vite.Key] = 1, 2
	m.st.EventSessions[ci.Key], m.st.EventSessions[review.Key] = 3, 4
	m.refreshSessions()
	// Restore marks working sessions interrupted; show them as live.
	m.sessions[sessionIndex(m, 3)].Status = claude.StatusRunning
	w := &m.sessions[sessionIndex(m, 4)]
	w.Status = claude.StatusWaiting
	w.Pending = []claude.Request{
		{SessionID: 4, RequestID: "p", ToolName: "Bash", Summary: `gh pr review 12 --repo other-org/cli --request-changes --body "Looks great overall…"`,
			Description: "Submit the review requesting changes"},
		{SessionID: 4, RequestID: "q", ToolName: "AskUserQuestion", Questions: []claude.Question{{
			Header:   "Review",
			Question: "How should I submit the review of other-org/cli#12?",
			Options: []claude.Option{
				{Label: "Request changes", Description: "Ask for unknown profiles to fail with an error"},
				{Label: "Approve with a comment", Description: "Approve, and suggest the fix as a follow-up"},
				{Label: "Comment only", Description: "Leave the review as comments without a verdict"},
			}}}},
	}
	// Pending requests open on their own when a dialog closes; the
	// permission and question shots open them explicitly.
	m.deferred["p"], m.deferred["q"] = true, true
	m.checks = []doctor.Check{
		{Name: "GitHub CLI", Detail: "gh version 2.102.0 (2026-09-30)"},
		{Name: "GitHub login", Detail: "@demo-dev · scopes: gist, read:org, repo, workflow"},
		{Name: "Notifications", Detail: "readable, for review requests, mentions and assignments"},
		{Name: "Claude Code", Detail: "2.1.306 (Claude Code)"},
		{Name: "git", Detail: "git version 2.54.0"},
		{Name: "Workspace", Detail: "~/.local/share/fast-lane/workspaces"},
		{Name: "Code folders", Status: doctor.Warn, Detail: "~/Code (14 checkouts)",
			Fix: "~/src doesn't exist; remove it from Code folders below."},
		{Name: "Config file", Detail: "~/.config/fast-lane/config.json"},
	}
	m.cfg.CodeDirs = []string{"~/Code", "~/src"}
	return m
}
