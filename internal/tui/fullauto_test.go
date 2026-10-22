package tui

import (
	"strings"
	"testing"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestCycleRepoIntoFullAuto(t *testing.T) {
	m, gh := newTestModel(t)
	run(m, m.load())
	m.cfg.Watch("me/app")
	keys(m, "3")
	m.repoCursor = 0
	keys(m, "a")
	if rc := m.cfg.Repo("me/app"); !rc.AutoRun || rc.FullAuto {
		t.Fatal("first a: auto-run")
	}
	keys(m, "a")
	if m.dialog == nil || !strings.Contains(m.View(), "Turn on full auto for me/app?") || m.cfg.Repo("me/app").FullAuto {
		t.Fatalf("second a should ask before full auto:\n%s", m.View())
	}
	keys(m, "y")
	if rc := m.cfg.Repo("me/app"); !rc.FullAuto || !rc.AutoRun {
		t.Fatal("y should turn on full auto")
	}
	if len(gh.protected) != 1 || gh.protected[0] != "me/app" || !strings.Contains(m.status, "added rulesets") {
		t.Fatalf("full auto should protect the repo's history: %v, status %q", gh.protected, m.status)
	}
	v := m.View()
	if !strings.Contains(v, "⚡ full auto: 1 repo") || !strings.Contains(v, "FULL") {
		t.Fatalf("full auto should show in the header and on the repo:\n%s", v)
	}
	keys(m, "a")
	if rc := m.cfg.Repo("me/app"); rc.AutoRun || rc.FullAuto {
		t.Fatal("third a: off")
	}

	// Declining leaves auto-run as it was.
	keys(m, "a", "a", "n")
	if rc := m.cfg.Repo("me/app"); !rc.AutoRun || rc.FullAuto {
		t.Fatal("n should keep auto-run without full auto")
	}
}

func TestFullAutoRunsEveryTriggerAndSaysSo(t *testing.T) {
	m, gh := newTestModel(t)
	m.login = "me"
	rc := m.cfg.Watch("me/app")
	rc.AutoRun, rc.FullAuto = true, true
	pr := ev(2, config.KindPullRequest)
	pr.HeadSHA, pr.HeadAuthor = "a1", "amy"
	gh.events = []github.Event{pr}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))

	// No rule names "pushed", but full auto runs it anyway, in full auto.
	pr.HeadSHA = "a2"
	gh.events = []github.Event{pr}
	run(m, m.pollRepo(*m.cfg.Repo("me/app")))
	ss := m.mgr.Snapshot()
	if len(ss) != 1 || !ss[0].FullAuto {
		t.Fatalf("a push on a full-auto repo should start a full-auto session: %+v", ss)
	}

	m.tab = tabEvents
	m.eventCursor = 0
	m.mgr.Restore(nil)
	m.st.EventSessions = map[string]int{}
	m.refreshSessions()
	keys(m, "enter")
	if !strings.Contains(m.View(), "⚡ Full auto: Claude decides and acts on its own") {
		t.Fatalf("the start dialog should say it's full auto:\n%s", m.View())
	}
}

func TestRefusalReachesTheStatusLine(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(sessionMsg(claude.Update{SessionID: 3, Refused: "Bash: gh repo delete me/app --yes"}))
	if !m.statusErr || !strings.Contains(m.status, "Full auto refused, in session #3: Bash: gh repo delete") {
		t.Fatalf("status %q", m.status)
	}
}

func TestInboxCycleWithFullAuto(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.cfg.Inbox.Enabled || m.cfg.Inbox.AutoRun {
		t.Fatal("the inbox starts out listing only")
	}
	keys(m, "4", "i", "i")
	if !m.cfg.Inbox.Enabled || !m.cfg.Inbox.AutoRun {
		t.Fatal("list only, then off, then auto-run")
	}
	keys(m, "i")
	if m.dialog == nil || !strings.Contains(m.View(), "Turn on full auto for the inbox?") {
		t.Fatal("from auto-run, i should ask before full auto")
	}
	keys(m, "y")
	if !m.cfg.Inbox.FullAuto || !strings.Contains(m.View(), "full auto") {
		t.Fatal("y: inbox full auto")
	}
	keys(m, "i")
	if m.cfg.Inbox.FullAuto || m.cfg.Inbox.AutoRun || !m.cfg.Inbox.Enabled {
		t.Fatal("then list only")
	}
	keys(m, "i")
	if m.cfg.Inbox.Enabled {
		t.Fatal("then off")
	}
	keys(m, "i")
	if !m.cfg.Inbox.Enabled || !m.cfg.Inbox.AutoRun || m.cfg.Inbox.FullAuto {
		t.Fatal("then back to auto-run")
	}
}
