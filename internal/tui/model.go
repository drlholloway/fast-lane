// Package tui implements the Fast Lane terminal interface.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/drlholloway/fast-lane/internal/auto"
	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/prompt"
	"github.com/drlholloway/fast-lane/internal/state"
)

type tab int

const (
	tabEvents tab = iota
	tabSessions
	tabRepos
)

var tabNames = []string{"Events", "Sessions", "Repos"}

type (
	loadedMsg struct {
		login   string
		repos   []github.Repo
		starred []github.Repo
		myPRs   []github.Event
		err     error
	}
	polledMsg struct {
		repo   string
		events []github.Event
		err    error
	}
	inboxMsg struct {
		items []github.InboxItem
		at    time.Time
		err   error
	}
	pollTickMsg    struct{}
	refreshTickMsg time.Time
	sessionMsg     claude.Update
	openedMsg      struct{ err error }
)

// Model is the root Bubble Tea model.
type Model struct {
	cfg *config.Config
	st  *state.State
	gh  github.Client
	mgr *claude.Manager
	// Browse opens a URL; swapped out in tests.
	Browse func(url string) error

	width, height int
	tab           tab
	showHelp      bool
	login         string
	loading       bool
	loadErr       error
	spinner       spinner.Model
	status        string
	statusErr     bool
	statusAt      time.Time
	dirty         bool
	started       time.Time

	// Repos tab.
	repos      []repoRow
	repoSource repoSource
	repoCursor int
	filter     textinput.Model
	filtering  bool

	// Events tab.
	events      map[string][]github.Event
	feed        []github.Event
	fresh       map[string]bool
	eventCursor int
	polling     int
	lastPoll    time.Time
	nextPoll    time.Time
	pollErrs    map[string]string

	// Sessions tab.
	sessions   []claude.Session
	sessCursor int
	transcript viewport.Model
	follow     bool

	dialog   dialog
	deferred map[string]bool
	// pending holds changes for sessions that were busy, by session ID.
	pending map[int]pendingRun
}

// New builds the root model.
func New(cfg *config.Config, st *state.State, gh github.Client, mgr *claude.Manager) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = sGood
	fi := textinput.New()
	fi.Placeholder = "filter repos"
	fi.Prompt = "/ "
	return &Model{
		cfg:      cfg,
		st:       st,
		gh:       gh,
		mgr:      mgr,
		Browse:   openURL,
		loading:  true,
		spinner:  sp,
		filter:   fi,
		events:   map[string][]github.Event{},
		fresh:    map[string]bool{},
		pollErrs: map[string]string{},
		deferred: map[string]bool{},
		pending:  map[int]pendingRun{},
		follow:   true,
		started:  time.Now(),
		sessions: mgr.Snapshot(),
	}
}

func openURL(url string) error {
	return browser.New("", io.Discard, io.Discard).Browse(url)
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.load(), m.waitSession(), refreshTick(), m.pollAll())
}

func refreshTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return refreshTickMsg(t) })
}

func (m *Model) waitSession() tea.Cmd {
	ch := m.mgr.Updates()
	return func() tea.Msg { return sessionMsg(<-ch) }
}

func (m *Model) load() tea.Cmd {
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var msg loadedMsg
		msg.login, msg.err = gh.CurrentUser(ctx)
		if msg.err != nil {
			return msg
		}
		msg.repos, msg.err = gh.ListRepos(ctx)
		if msg.err != nil {
			return msg
		}
		// Starred repos and open PRs are extras; keep going without them.
		msg.starred, _ = gh.ListStarred(ctx)
		msg.myPRs, _ = gh.ListMyOpenPRs(ctx, msg.login)
		return msg
	}
}

func (m *Model) pollRepo(r config.Repo) tea.Cmd {
	gh := m.gh
	m.polling++
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		evs, err := gh.FetchEvents(ctx, r.FullName, r.Events)
		return polledMsg{repo: r.FullName, events: evs, err: err}
	}
}

func (m *Model) pollAll() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.cfg.Repos)+1)
	for _, r := range m.cfg.Repos {
		cmds = append(cmds, m.pollRepo(r))
	}
	m.lastPoll = time.Now()
	m.nextPoll = m.lastPoll.Add(m.cfg.PollInterval.Duration)
	cmds = append(cmds, m.pollInbox())
	cmds = append(cmds, tea.Tick(m.cfg.PollInterval.Duration, func(time.Time) tea.Msg { return pollTickMsg{} }))
	return tea.Batch(cmds...)
}

// inboxOverlap re-reads a little of the previous window so notifications
// updated during a poll are not missed; Fire drops the repeats.
const inboxOverlap = time.Minute

// pollInbox reads review requests, mentions and assignments. The very first
// poll only records the time, so notifications from before Fast Lane started
// watching the inbox never fire.
func (m *Model) pollInbox() tea.Cmd {
	if !m.cfg.Inbox.Enabled || m.login == "" {
		return nil
	}
	if m.st.InboxSince.IsZero() {
		m.st.InboxSince = time.Now()
		m.dirty = true
		return nil
	}
	gh, me, since := m.gh, m.login, m.st.InboxSince.Add(-inboxOverlap)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		at := time.Now()
		items, err := gh.Inbox(ctx, me, since)
		return inboxMsg{items: items, at: at, err: err}
	}
}

func (m *Model) setStatus(err bool, format string, args ...any) {
	m.status = fmt.Sprintf(format, args...)
	m.statusErr = err
	m.statusAt = time.Now()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.dialog != nil {
			m.dialog.resize(m.width, m.height)
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case loadedMsg:
		m.loading = false
		m.loadErr = msg.err
		if msg.err != nil {
			m.setStatus(true, "Could not load repositories: %v", msg.err)
			break
		}
		m.login = msg.login
		m.repos = mergeRepos(msg.repos, msg.starred, msg.myPRs, msg.login)
		cmds = append(cmds, m.pollInbox())

	case pollTickMsg:
		cmds = append(cmds, m.pollAll())

	case polledMsg:
		cmds = append(cmds, m.onPolled(msg))

	case inboxMsg:
		cmds = append(cmds, m.onInbox(msg))

	case sessionMsg:
		m.refreshSessions()
		m.deliverPending()
		cmds = append(cmds, m.waitSession())
		if msg.NeedsUser {
			cmds = append(cmds, bell)
		}
		cmds = append(cmds, m.maybeOpenPending())

	case refreshTickMsg:
		m.refreshSessions()
		m.deliverPending()
		cmds = append(cmds, refreshTick(), m.maybeOpenPending())
		if m.dirty {
			m.save()
		}
		if m.status != "" && !m.statusErr && time.Since(m.statusAt) > 8*time.Second {
			m.status = ""
		}

	case openedMsg:
		if msg.err != nil {
			m.setStatus(true, "Could not open browser: %v", msg.err)
		}

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if _, confirming := m.dialog.(*confirmDialog); !confirming {
				m.dialog = nil
				cmds = append(cmds, m.quit())
				break
			}
		}
		if m.dialog != nil {
			d, cmd := m.dialog.update(m, msg)
			m.dialog = d
			cmds = append(cmds, cmd)
			if d == nil {
				cmds = append(cmds, m.maybeOpenPending())
			}
			break
		}
		cmds = append(cmds, m.handleKey(msg))

	default:
		if m.dialog != nil {
			cmds = append(cmds, m.dialog.passthrough(msg))
		}
	}
	return m, tea.Batch(cmds...)
}

func bell() tea.Msg {
	_, _ = os.Stdout.Write([]byte("\a"))
	return nil
}

func (m *Model) save() {
	m.st.Sessions = m.mgr.Persistable()
	if err := m.st.Save(); err != nil {
		m.setStatus(true, "Could not save state: %v", err)
		return
	}
	m.dirty = false
}

// onPolled merges fresh events, detects what changed on known ones, and
// starts or resumes sessions for changes that auto-run.
func (m *Model) onPolled(msg polledMsg) tea.Cmd {
	m.polling--
	if msg.err != nil {
		m.pollErrs[msg.repo] = msg.err.Error()
	} else {
		delete(m.pollErrs, msg.repo)
	}
	rc := m.cfg.Repo(msg.repo)
	if rc == nil {
		// Unwatched while the poll was in flight.
		return nil
	}
	m.events[strings.ToLower(msg.repo)] = msg.events
	now := time.Now()
	changed, started, filtered, capped := 0, 0, 0, 0
	for _, ev := range msg.events {
		changes := m.changesFor(ev, now)
		if len(changes) == 0 {
			continue
		}
		changed++
		m.fresh[ev.Key] = true
		delete(m.st.Dismissed, ev.Key)
		c, rule, ok := m.autoChange(*rc, ev, changes)
		switch {
		case !ok && rc.AutoRun:
			filtered++
		case !ok:
		case m.st.AutoRuns[ev.Key] >= m.cfg.MaxAutoRuns:
			capped++
		default:
			if err := m.runChange(ev, c, rule); err != nil {
				m.setStatus(true, "Auto-run %s (%s) failed: %v", ev.Key, c.Trigger, err)
				continue
			}
			m.st.AutoRuns[ev.Key]++
			started++
		}
	}
	if msg.err == nil {
		m.st.MarkBaselined(msg.repo)
	}
	m.dirty = true
	m.rebuildFeed()
	if changed == 0 {
		return nil
	}
	text := fmt.Sprintf("%s: %d new or changed", msg.repo, changed)
	if started > 0 {
		text += fmt.Sprintf(", ran Claude on %d", started)
	}
	if filtered > 0 {
		text += fmt.Sprintf(", %d didn't match auto rules", filtered)
	}
	if capped > 0 {
		text += fmt.Sprintf(", %d reached max_auto_runs", capped)
	}
	m.setStatus(false, "%s", text)
	if started == changed {
		return nil
	}
	return bell
}

// changesFor records ev's snapshot and returns the changes that fire: opened
// for an item first seen after its repo's baseline, otherwise whatever
// Detect finds since the last poll, minus changes that already fired.
func (m *Model) changesFor(ev github.Event, now time.Time) []github.Change {
	isNew := m.st.Observe(ev.Repo, ev.Key, now)
	prev, had := m.st.Snapshots[ev.Key]
	var changes []github.Change
	switch {
	case isNew:
		changes = []github.Change{github.Opened(ev)}
	case had && m.login == "":
		// Until we know who the user is we cannot ignore their own activity,
		// so keep the old snapshot and compare on the next poll.
		return nil
	case had:
		changes = github.Detect(prev, ev, m.login)
	}
	m.st.Snapshots[ev.Key] = ev.Snapshot()
	var out []github.Change
	for _, c := range changes {
		if m.st.Fire(ev.Key, c) {
			out = append(out, c)
		}
	}
	return out
}

// autoChange picks the first change, in trigger order, that auto-runs.
func (m *Model) autoChange(rc config.Repo, ev github.Event, changes []github.Change) (github.Change, config.Rule, bool) {
	for _, c := range changes {
		if rule, ok := auto.Decide(m.cfg, rc, ev, c.Trigger); ok {
			return c, rule, true
		}
	}
	return github.Change{}, config.Rule{}, false
}

// onInbox records review requests, mentions and assignments and runs the
// ones that auto-run. Items in watched repos already appear in the feed from
// their repo's poll.
func (m *Model) onInbox(msg inboxMsg) tea.Cmd {
	if msg.err != nil {
		m.pollErrs["inbox"] = msg.err.Error()
		return nil
	}
	delete(m.pollErrs, "inbox")
	m.st.InboxSince = msg.at
	changed, started, capped := 0, 0, 0
	for _, it := range msg.items {
		ev, c := it.Event, it.Change
		rc := m.cfg.Repo(ev.Repo)
		if rc == nil {
			m.st.Inbox[ev.Key] = ev
		}
		if !m.st.Fire(ev.Key, c) {
			continue
		}
		changed++
		m.fresh[ev.Key] = true
		delete(m.st.Dismissed, ev.Key)
		rule, ok := auto.Decide(m.cfg, m.repoFor(ev), ev, c.Trigger)
		switch {
		case !ok:
		case m.st.AutoRuns[ev.Key] >= m.cfg.MaxAutoRuns:
			capped++
		default:
			if err := m.runChange(ev, c, rule); err != nil {
				m.setStatus(true, "Auto-run %s (%s) failed: %v", ev.Key, c.Trigger, err)
				continue
			}
			m.st.AutoRuns[ev.Key]++
			started++
		}
	}
	m.st.PruneInbox(time.Now().Add(-inboxMaxAge))
	m.dirty = true
	m.rebuildFeed()
	if changed == 0 {
		return nil
	}
	text := fmt.Sprintf("Inbox: %d review request(s), mention(s) or assignment(s)", changed)
	if started > 0 {
		text += fmt.Sprintf(", ran Claude on %d", started)
	}
	if capped > 0 {
		text += fmt.Sprintf(", %d reached max_auto_runs", capped)
	}
	m.setStatus(false, "%s", text)
	if started == changed {
		return nil
	}
	return bell
}

// inboxMaxAge is how long an inbox item stays in the feed without activity.
const inboxMaxAge = 14 * 24 * time.Hour

// repoFor returns ev's watch settings, or bare settings for a repo the user
// does not watch, which then follows the global auto rules.
func (m *Model) repoFor(ev github.Event) config.Repo {
	if rc := m.cfg.Repo(ev.Repo); rc != nil {
		return *rc
	}
	return config.Repo{FullName: ev.Repo}
}

// pendingRun is a change waiting for its session to finish a turn.
type pendingRun struct {
	text  string
	allow []config.Action
}

// runChange acts on change c. It resumes the item's last session so Claude
// keeps what it learned, or starts one when there is none to resume. A
// session that is still working gets the change when it finishes its turn.
func (m *Model) runChange(ev github.Event, c github.Change, rule config.Rule) error {
	text, err := prompt.Render(m.cfg, ev, c, rule.Prompt)
	if err != nil {
		return err
	}
	s, ok := m.sessionFor(ev.Key)
	switch {
	case !ok || s.ClaudeID == "":
		return m.startSessionWithPrompt(ev, text, rule.Allow)
	case s.Status.Active() || s.Status == claude.StatusQueued:
		m.pending[s.ID] = pendingRun{text: text, allow: rule.Allow}
	default:
		if err := m.mgr.Resume(s.ID, text, rule.Allow); err != nil {
			return err
		}
		m.refreshSessions()
	}
	delete(m.fresh, ev.Key)
	return nil
}

// deliverPending resumes sessions that finished their turn with a change
// waiting. Changes for sessions the user stopped are dropped.
func (m *Model) deliverPending() {
	for id, p := range m.pending {
		var s claude.Session
		for _, x := range m.sessions {
			if x.ID == id {
				s = x
			}
		}
		switch {
		case s.ID == 0 || s.Status == claude.StatusStopped:
			delete(m.pending, id)
		case s.Status.Active() || s.Status == claude.StatusQueued:
		default:
			delete(m.pending, id)
			if err := m.mgr.Resume(id, p.text, p.allow); err != nil {
				m.setStatus(true, "Could not resume session %d: %v", id, err)
			}
		}
	}
}

func (m *Model) hasSession(key string) bool {
	id, ok := m.st.EventSessions[key]
	if !ok {
		return false
	}
	for _, s := range m.sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

func (m *Model) sessionFor(key string) (claude.Session, bool) {
	id, ok := m.st.EventSessions[key]
	if !ok {
		return claude.Session{}, false
	}
	for _, s := range m.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return claude.Session{}, false
}

// manualChange returns the change and rule a session started by hand should
// use: the item's last change, or opened, and whatever rule governs it.
// Rules shape manual sessions too, not only auto-run ones.
func (m *Model) manualChange(ev github.Event) (github.Change, config.Rule) {
	c, ok := m.st.LastChange[ev.Key]
	if !ok {
		c = github.Opened(ev)
	}
	rule, _ := auto.RuleFor(m.cfg, m.repoFor(ev), ev, c.Trigger)
	return c, rule
}

func (m *Model) startSessionWithPrompt(ev github.Event, text string, allow []config.Action) error {
	id := m.mgr.StartPrompt(ev, text, allow)
	m.st.EventSessions[ev.Key] = id
	delete(m.fresh, ev.Key)
	m.dirty = true
	m.refreshSessions()
	return nil
}

func (m *Model) rebuildFeed() {
	var feed []github.Event
	for _, r := range m.cfg.Repos {
		for _, ev := range m.events[strings.ToLower(r.FullName)] {
			if !r.Watches(ev.Kind) || m.st.IsDismissed(ev.Key, ev.UpdatedAt) {
				continue
			}
			feed = append(feed, ev)
		}
	}
	for _, ev := range m.st.Inbox {
		if m.cfg.Repo(ev.Repo) == nil && !m.st.IsDismissed(ev.Key, ev.UpdatedAt) {
			feed = append(feed, ev)
		}
	}
	github.SortEvents(feed)
	m.feed = feed
	m.eventCursor = clamp(m.eventCursor, 0, len(feed)-1)
}

func (m *Model) refreshSessions() {
	m.sessions = m.mgr.Snapshot()
	m.sessCursor = clamp(m.sessCursor, 0, len(m.sessions)-1)
	m.dirty = true
	m.updateTranscript()
}

// maybeOpenPending pops a dialog for the oldest request nobody deferred.
func (m *Model) maybeOpenPending() tea.Cmd {
	if m.dialog != nil {
		return nil
	}
	for i := len(m.sessions) - 1; i >= 0; i-- {
		for _, r := range m.sessions[i].Pending {
			if !m.deferred[r.RequestID] {
				m.openRequest(m.sessions[i], r)
				return nil
			}
		}
	}
	return nil
}

func (m *Model) openRequest(s claude.Session, r claude.Request) {
	if r.IsQuestion() {
		m.dialog = newQuestionDialog(s, r, m.width, m.height)
	} else {
		m.dialog = newPermissionDialog(s, r, m.width, m.height)
	}
}

func (m *Model) waitingCount() (sessions, requests int) {
	for _, s := range m.sessions {
		if len(s.Pending) > 0 {
			sessions++
			requests += len(s.Pending)
		}
	}
	return
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.filtering {
		return m.updateFilter(msg)
	}
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()
	case "?":
		m.showHelp = !m.showHelp
		return nil
	case "tab":
		m.tab = (m.tab + 1) % 3
		return nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		return nil
	case "1":
		m.tab = tabEvents
		return nil
	case "2":
		m.tab = tabSessions
		return nil
	case "3":
		m.tab = tabRepos
		return nil
	case "R":
		m.setStatus(false, "Refreshing…")
		return tea.Batch(m.pollAll(), m.load())
	}
	switch m.tab {
	case tabEvents:
		return m.eventsKey(msg)
	case tabSessions:
		return m.sessionsKey(msg)
	case tabRepos:
		return m.reposKey(msg)
	}
	return nil
}

func (m *Model) quit() tea.Cmd {
	n := 0
	for _, s := range m.sessions {
		if s.Status.Active() || s.Status == claude.StatusQueued {
			n++
		}
	}
	if n == 0 {
		m.save()
		return tea.Quit
	}
	m.dialog = newConfirmDialog(
		fmt.Sprintf("%d Claude session(s) are still working.\nQuitting stops them; you can reply later to resume.\n\nQuit anyway?", n),
		func(m *Model) tea.Cmd {
			m.mgr.Shutdown()
			m.save()
			return tea.Quit
		}, m.width)
	return nil
}

func (m *Model) layout() {
	w, h := m.transcriptSize()
	m.transcript.Width, m.transcript.Height = w, h
	m.updateTranscript()
}

func (m *Model) bodyHeight() int {
	h := m.height - 2 - lipgloss.Height(m.footerView()) // header, tabs, footer
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(m.tabsView())
	b.WriteString("\n")

	bodyH := m.bodyHeight()
	var body string
	if m.dialog != nil {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.dialog.view(m))
	} else {
		switch m.tab {
		case tabEvents:
			body = m.eventsView(bodyH)
		case tabSessions:
			body = m.sessionsView(bodyH)
		case tabRepos:
			body = m.reposView(bodyH)
		}
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	b.WriteString(body)
	b.WriteString("\n")
	b.WriteString(m.footerView())
	return b.String()
}

func (m *Model) headerView() string {
	parts := []string{sTitle.Render("Fast Lane")}
	if m.login != "" {
		parts = append(parts, sMuted.Render("@"+m.login))
	}
	parts = append(parts, sMuted.Render(fmt.Sprintf("watching %d", len(m.cfg.Repos))))
	running := 0
	for _, s := range m.sessions {
		if s.Status.Active() {
			running++
		}
	}
	if running > 0 {
		parts = append(parts, sGood.Render(fmt.Sprintf("%s %d running", m.spinner.View(), running)))
	}
	if ws, _ := m.waitingCount(); ws > 0 {
		parts = append(parts, sAlert.Render(fmt.Sprintf("! %d need you", ws)))
	}
	switch {
	case m.loading || m.polling > 0:
		parts = append(parts, sMuted.Render(m.spinner.View()+" syncing"))
	case !m.nextPoll.IsZero() && len(m.cfg.Repos) > 0:
		parts = append(parts, sMuted.Render("next poll "+time.Until(m.nextPoll).Round(time.Second).String()))
	}
	return strings.Join(parts, sMuted.Render("  ·  "))
}

func (m *Model) tabsView() string {
	var parts []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		switch tab(i) {
		case tabEvents:
			label += fmt.Sprintf(" (%d)", len(m.feed))
		case tabSessions:
			label += fmt.Sprintf(" (%d)", len(m.sessions))
		}
		if tab(i) == m.tab {
			parts = append(parts, sTabOn.Render(label))
		} else {
			parts = append(parts, sTabOff.Render(label))
		}
	}
	return strings.Join(parts, "   ")
}

func (m *Model) footerView() string {
	var help string
	switch {
	case m.dialog != nil:
		help = m.dialog.help()
	case m.filtering:
		help = helpLine("enter", "apply", "esc", "clear")
	case m.tab == tabEvents:
		help = helpLine("enter", "run Claude", "s", "session", "o", "open", "x", "dismiss", "R", "refresh", "?", "help", "q", "quit")
	case m.tab == tabSessions:
		help = helpLine("enter", "reply/answer", "x", "stop", "o", "open", "pgup/pgdn", "scroll", "?", "help", "q", "quit")
	case m.tab == tabRepos:
		help = helpLine("space", "watch", "a", "auto-run", "e", "events", "f", "source", "/", "filter", "?", "help", "q", "quit")
	}
	if m.showHelp {
		help = helpLine("tab/1-3", "switch tabs", "j/k", "move", "g/G", "top/bottom", "R", "refresh now") + "\n" +
			sMuted.Render("Config: "+m.cfg.Path()) + "\n" + help
	}
	if m.status != "" {
		st := sGood
		if m.statusErr {
			st = sBad
		}
		return st.Render(fit(m.status, m.width)) + "\n" + help
	}
	return help
}

// moveCursor applies list navigation keys and reports whether it handled msg.
func moveCursor(msg tea.KeyMsg, cursor *int, total, page int) bool {
	switch msg.String() {
	case "up", "k":
		*cursor--
	case "down", "j":
		*cursor++
	case "pgup", "ctrl+u":
		*cursor -= page
	case "pgdown", "ctrl+d":
		*cursor += page
	case "home", "g":
		*cursor = 0
	case "end", "G":
		*cursor = total - 1
	default:
		return false
	}
	*cursor = clamp(*cursor, 0, total-1)
	return true
}

func sortedKeys[V any](mp map[string]V) []string {
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
