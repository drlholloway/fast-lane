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
