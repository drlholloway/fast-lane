package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/state"
)

type fakeGH struct{ events []github.Event }

func (f *fakeGH) CurrentUser(context.Context) (string, error) { return "me", nil }
func (f *fakeGH) ListRepos(context.Context) ([]github.Repo, error) {
	return []github.Repo{{FullName: "me/app", Description: "the app"}, {FullName: "org/lib"}}, nil
}
func (f *fakeGH) ListStarred(context.Context) ([]github.Repo, error) {
	return []github.Repo{{FullName: "cool/thing"}}, nil
}
func (f *fakeGH) ListMyOpenPRs(context.Context, string) ([]github.Event, error) {
	return []github.Event{{Repo: "other/proj"}}, nil
}
func (f *fakeGH) FetchEvents(_ context.Context, repo string, _ []config.EventKind) ([]github.Event, error) {
	return f.events, nil
}

func ev(n int, kind config.EventKind) github.Event {
	return github.Event{Key: github.EventKey("me/app", kind, n), Repo: "me/app", Kind: kind, Number: n,
		Title: "thing " + string(rune('A'+n)), UpdatedAt: time.Now().Add(-time.Duration(n) * time.Hour)}
}

func newTestModel(t *testing.T) (*Model, *fakeGH) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkspaceDir = filepath.Join(dir, "ws")
	st, _ := state.Load(filepath.Join(dir, "state.json"))
	mgr := claude.NewManager(cfg, "")
	mgr.SetRunner(func(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	})
	t.Cleanup(mgr.Shutdown)
	gh := &fakeGH{}
	m := New(cfg, st, gh, mgr)
	m.Browse = func(string) error { return nil }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, gh
}

func run(m *Model, cmd tea.Cmd) {
	// Execute commands that resolve immediately, skipping ticks and channel waits.
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				run(m, c)
			}
			return
		}
		if msg != nil {
			_, next := m.Update(msg)
			run(m, next)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestWatchRepoAndBaseline(t *testing.T) {
	m, gh := newTestModel(t)
	run(m, m.load())
	if len(m.repos) != 4 {
		t.Fatalf("expected merged repos (owned, org, starred, PR), got %+v", m.repos)
	}

	gh.events = []github.Event{ev(1, config.KindIssue)}
	m.tab = tabRepos
	_, cmd := m.Update(press(" ")) // watch me/app (first row)
	run(m, cmd)
	if m.cfg.Repo("me/app") == nil {
		t.Fatal("space should watch the selected repo")
	}
	if len(m.feed) != 1 || m.fresh[m.feed[0].Key] {
		t.Fatalf("baseline events are listed but not marked new: feed=%v fresh=%v", m.feed, m.fresh)
	}

	gh.events = append(gh.events, ev(2, config.KindPullRequest))
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if len(m.feed) != 2 || !m.fresh[github.EventKey("me/app", config.KindPullRequest, 2)] {
		t.Fatalf("second poll should flag the new PR: %v", m.fresh)
	}

	m.tab = tabEvents
	m.eventCursor = 0
	m.Update(press("x"))
	if len(m.feed) != 1 {
		t.Fatalf("dismiss should hide the event, feed=%v", m.feed)
	}

	view := m.View()
	for _, want := range []string{"Fast Lane", "@me", "Events (1)", "thing C"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestAutoRunStartsSessionForNewEvents(t *testing.T) {
	m, gh := newTestModel(t)
	m.cfg.Watch("me/app").AutoRun = true
	gh.events = []github.Event{ev(1, config.KindIssue)}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if len(m.mgr.Snapshot()) != 0 {
		t.Fatal("events present at the baseline must not auto-run")
	}
	gh.events = append(gh.events, ev(3, config.KindDependabot))
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	ss := m.mgr.Snapshot()
	if len(ss) != 1 || ss[0].Event.Number != 3 {
		t.Fatalf("expected one auto session for #3, got %+v", ss)
	}
	if _, ok := m.st.EventSessions[github.EventKey("me/app", config.KindDependabot, 3)]; !ok {
		t.Fatal("session not linked to event")
	}
}

func TestAutoRulesFilterNewEvents(t *testing.T) {
	m, gh := newTestModel(t)
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{"dependabot_alert": {{MinSeverity: "high"}}}
	m.cfg.Watch("me/app").AutoRun = true
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))

	low, high := ev(4, config.KindDependabot), ev(5, config.KindDependabot)
	low.Severity, high.Severity = "low", "critical"
	gh.events = []github.Event{ev(2, config.KindPullRequest), low, high}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	ss := m.mgr.Snapshot()
	if len(ss) != 1 || ss[0].Event.Number != 5 {
		t.Fatalf("only the critical alert should auto-run, got %+v", ss)
	}
	if !strings.Contains(m.status, "2 didn't match auto rules") {
		t.Fatalf("status should report filtered events: %q", m.status)
	}
	if len(m.feed) != 3 {
		t.Fatalf("filtered events still belong in the feed: %v", m.feed)
	}
}

func TestRulePromptAndActionsShapeSessions(t *testing.T) {
	m, gh := newTestModel(t)
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{
		"dependabot_alert": {{Prompt: "bump #{{.Number}}", Allow: []config.Action{config.ActionPushBranch, config.ActionOpenPR}}},
	}
	m.cfg.Watch("me/app").AutoRun = true
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	gh.events = []github.Event{ev(6, config.KindDependabot)}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	ss := m.mgr.Snapshot()
	if len(ss) != 1 || len(ss[0].Allow) != 2 {
		t.Fatalf("auto session should carry the rule's actions: %+v", ss)
	}

	// Starting by hand shows the same rule in the start dialog.
	m.cfg.Repo("me/app").AutoRun = false
	gh.events = append(gh.events, ev(7, config.KindDependabot))
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	m.tab = tabEvents
	for i, e := range m.feed {
		if e.Number == 7 {
			m.eventCursor = i
		}
	}
	m.Update(press("enter"))
	view := m.View()
	if !strings.Contains(view, "bump #7") || !strings.Contains(view, "Allowed without asking: push_branch, open_pr") {
		t.Fatalf("start dialog should show the rule's prompt and actions:\n%s", view)
	}
}

func TestPermissionDialogFlow(t *testing.T) {
	m, _ := newTestModel(t)
	s := claude.Session{ID: 1, Event: ev(1, config.KindIssue), Status: claude.StatusWaiting}
	r := claude.Request{SessionID: 1, RequestID: "r", ToolName: "Bash", Summary: "go test ./..."}
	m.openRequest(s, r)
	if !strings.Contains(m.View(), "go test ./...") {
		t.Fatal("permission dialog should show the command")
	}
	m.Update(press("esc"))
	if m.dialog != nil || !m.deferred["r"] {
		t.Fatal("esc should defer the request")
	}

	q := claude.Request{SessionID: 1, RequestID: "q", ToolName: "AskUserQuestion",
		Questions: []claude.Question{{Question: "Ship it?", Options: []claude.Option{{Label: "Yes"}, {Label: "No"}}}}}
	m.openRequest(s, q)
	m.Update(press("2"))
	if d := m.dialog.(*questionDialog); d.cursor != 1 {
		t.Fatalf("number key should select option, cursor=%d", d.cursor)
	}
	if !strings.Contains(m.View(), "Ship it?") {
		t.Fatal("question not rendered")
	}
}

func failingPR(n int, sha, checks string) github.Event {
	e := ev(n, config.KindPullRequest)
	e.HeadSHA, e.HeadAuthor, e.Checks = sha, "amy", checks
	return e
}

func TestTriggerStartsSessionOnce(t *testing.T) {
	m, gh := newTestModel(t)
	m.login = "me"
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{
		"pull_request:checks_failed": {{Allow: []config.Action{config.ActionEdit, config.ActionUpdatePR}}},
	}
	m.cfg.Watch("me/app").AutoRun = true
	gh.events = []github.Event{failingPR(2, "a1", "SUCCESS")}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))

	gh.events = []github.Event{failingPR(2, "a1", "FAILURE")}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	ss := m.mgr.Snapshot()
	if len(ss) != 1 || len(ss[0].Allow) != 2 || !strings.Contains(m.status, "ran Claude on 1") {
		t.Fatalf("checks failing should start a session with the rule's actions: %+v, status %q", ss, m.status)
	}
	key := github.EventKey("me/app", config.KindPullRequest, 2)
	if m.st.LastChange[key].Trigger != config.TriggerChecksFailed || m.st.AutoRuns[key] != 1 {
		t.Fatalf("change not recorded: %+v, runs %d", m.st.LastChange[key], m.st.AutoRuns[key])
	}

	// Still failing on the same commit: nothing new happens.
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if len(m.mgr.Snapshot()) != 1 {
		t.Fatal("the same change must not fire twice")
	}

	// A new commit that fails again fires again, until max_auto_runs.
	m.cfg.MaxAutoRuns = 1
	gh.events = []github.Event{failingPR(2, "a2", "FAILURE")}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if len(m.mgr.Snapshot()) != 1 || !strings.Contains(m.status, "1 reached max_auto_runs") {
		t.Fatalf("max_auto_runs should stop further runs, status %q", m.status)
	}
}

func TestTriggerResumesFinishedSession(t *testing.T) {
	m, gh := newTestModel(t)
	m.login = "me"
	m.cfg.AutoRules = map[config.RuleKey]config.Rules{
		"pull_request:commented": {{Allow: []config.Action{config.ActionComment}}},
	}
	m.cfg.Watch("me/app").AutoRun = true
	pr := ev(3, config.KindPullRequest)
	gh.events = []github.Event{pr}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))

	m.mgr.Restore([]*claude.Session{{ID: 7, Event: pr, Status: claude.StatusIdle, ClaudeID: "c-7"}})
	m.st.EventSessions[pr.Key] = 7
	m.refreshSessions()

	// My own comment is ignored; someone else's resumes the session.
	pr.LastComment = github.Activity{ID: "C1", Author: "me", Body: "note to self"}
	gh.events = []github.Event{pr}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	if s := m.mgr.Snapshot()[0]; s.Status != claude.StatusIdle {
		t.Fatalf("my own comment must not trigger: %+v", s)
	}
	pr.LastComment = github.Activity{ID: "C2", Author: "bob", Body: "please rename x"}
	gh.events = []github.Event{pr}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))

	ss := m.mgr.Snapshot()
	if len(ss) != 1 || ss[0].ID != 7 {
		t.Fatalf("the change should resume session 7, not start another: %+v", ss)
	}
	last := ss[0].Lines[len(ss[0].Lines)-1].Text
	if !strings.Contains(last, "@bob commented") || !strings.Contains(last, "please rename x") || len(ss[0].Allow) != 1 {
		t.Fatalf("resume should send the trigger prompt and the rule's actions: %q %v", last, ss[0].Allow)
	}
}

func TestBusySessionGetsChangeAfterItsTurn(t *testing.T) {
	m, _ := newTestModel(t)
	pr := ev(4, config.KindPullRequest)
	m.mgr.Restore([]*claude.Session{{ID: 9, Event: pr, Status: claude.StatusIdle, ClaudeID: "c-9"}})
	m.refreshSessions()
	m.sessions[0].Status = claude.StatusRunning
	m.pending[9] = pendingRun{text: "CI failed again", allow: []config.Action{config.ActionUpdatePR}}
	m.deliverPending()
	if _, waiting := m.pending[9]; !waiting {
		t.Fatal("a running session should keep its pending change")
	}
	m.refreshSessions()
	m.deliverPending()
	if _, waiting := m.pending[9]; waiting {
		t.Fatal("an idle session should receive its pending change")
	}
	if s := m.mgr.Snapshot()[0]; s.Lines[len(s.Lines)-1].Text != "CI failed again" {
		t.Fatalf("pending change not delivered: %+v", s.Lines)
	}
}
