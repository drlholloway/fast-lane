package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/prompt"
	"github.com/drlholloway/fast-lane/internal/state"
)

type eventFilter int

const (
	// showFocus is the default: watched repos and mentions, in sections.
	showFocus eventFilter = iota
	showAll
	showNew
	showForMe
	showPRs
	showIssues
	showAlerts
)

var eventFilterNames = []string{"watched + mentions", "all", "new", "for me", "PRs", "issues", "alerts"}

type eventSort int

const (
	sortName eventSort = iota
	sortUpdated
	sortCreated
	sortSeverity
)

var eventSortNames = []string{"a–z", "updated", "created", "severity"}

// Sections of the default view, in display order.
const (
	secNone = iota - 1
	secWatched
	secMentionMe
	secMentionTeam
)

var sectionNames = []string{"Watched repos", "Mentions of me", "Team mentions"}

// section places ev in the default view: a mention of me, else a mention of
// a team I'm in, else an item from a watched repo, else nowhere.
func (m *Model) section(ev github.Event) int {
	mention := m.st.Mentions[ev.Key]
	if mention == "" {
		if c, ok := m.st.LastChange[ev.Key]; ok && c.Trigger == config.TriggerMentioned {
			mention = state.MentionMe // recorded before mentions were told apart
		}
	}
	switch {
	case mention == state.MentionMe:
		return secMentionMe
	case mention == state.MentionTeam:
		return secMentionTeam
	case m.cfg.Repo(ev.Repo) != nil:
		return secWatched
	}
	return secNone
}

// visibleEvents applies the Events tab's filter, search and sort to the feed.
func (m *Model) visibleEvents() []github.Event {
	q := strings.ToLower(strings.TrimSpace(m.eventQuery.Value()))
	var out []github.Event
	for _, ev := range m.feed {
		if m.showsEvent(ev) && (q == "" || strings.Contains(eventText(ev), q)) {
			out = append(out, ev)
		}
	}
	switch m.eventSort {
	case sortName:
		sort.SliceStable(out, func(i, j int) bool {
			a, b := strings.ToLower(out[i].Repo), strings.ToLower(out[j].Repo)
			if a != b {
				return a < b
			}
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		})
	case sortCreated:
		sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	case sortSeverity:
		sort.SliceStable(out, func(i, j int) bool {
			return config.SeverityRank(out[i].Severity) > config.SeverityRank(out[j].Severity)
		})
	}
	if m.eventFilter == showFocus {
		sort.SliceStable(out, func(i, j int) bool { return m.section(out[i]) < m.section(out[j]) })
	}
	return out
}

func (m *Model) showsEvent(ev github.Event) bool {
	switch m.eventFilter {
	case showFocus:
		return m.section(ev) != secNone
	case showNew:
		return m.fresh[ev.Key]
	case showForMe:
		return m.forMe(ev)
	case showPRs:
		return ev.Kind == config.KindPullRequest
	case showIssues:
		return ev.Kind == config.KindIssue
	case showAlerts:
		return ev.Kind == config.KindDependabot || ev.Kind == config.KindCodeScanning
	}
	return true
}

// forMe reports whether ev is in the inbox: a review request, mention or
// assignment, from any repo.
func (m *Model) forMe(ev github.Event) bool {
	if _, ok := m.st.Inbox[ev.Key]; ok {
		return true
	}
	if c, ok := m.st.LastChange[ev.Key]; ok && c.Trigger.Personal() {
		return true
	}
	for _, r := range ev.ReviewRequests {
		if strings.EqualFold(r, m.login) {
			return true
		}
	}
	return false
}

// eventText is what the search matches against.
func eventText(ev github.Event) string {
	return strings.ToLower(strings.Join(append([]string{ev.Repo, "#" + strconv.Itoa(ev.Number), ev.Title,
		ev.Author, ev.Kind.Short(), ev.Severity, ev.Branch}, ev.Labels...), " "))
}

func (m *Model) selectedEvent() (github.Event, bool) {
	if m.eventCursor < 0 || m.eventCursor >= len(m.shown) {
		return github.Event{}, false
	}
	return m.shown[m.eventCursor], true
}

func (m *Model) eventsKey(msg tea.KeyMsg) tea.Cmd {
	if moveCursor(msg, &m.eventCursor, len(m.shown), m.bodyHeight()-3) {
		return nil
	}
	switch msg.String() {
	case "/":
		m.filtering = true
		return m.eventQuery.Focus()
	case "esc":
		m.eventQuery.SetValue("")
		m.eventCursor = 0
		m.rebuildFeed()
		return nil
	case "f":
		m.eventFilter = (m.eventFilter + 1) % eventFilter(len(eventFilterNames))
		m.eventCursor = 0
		m.rebuildFeed()
		return nil
	case "S":
		m.eventSort = (m.eventSort + 1) % eventSort(len(eventSortNames))
		m.eventCursor = 0
		m.rebuildFeed()
		return nil
	}
	ev, ok := m.selectedEvent()
	if !ok {
		return nil
	}
	switch msg.String() {
	case "enter":
		if s, ok := m.sessionFor(ev.Key); ok && (s.Status.Active() || s.Status == claude.StatusQueued) {
			m.jumpToSession(s.ID)
			return nil
		}
		c, rule := m.manualChange(ev)
		text, err := prompt.Render(m.cfg, ev, c, rule.Prompt)
		if err != nil {
			m.setStatus(true, "%v", err)
			return nil
		}
		m.dialog = newStartDialog(ev, text, options(rule, m.fullAuto(ev, c)), m.location(ev.Repo), m.width, m.height)
	case "s":
		if s, ok := m.sessionFor(ev.Key); ok {
			m.jumpToSession(s.ID)
		} else {
			m.setStatus(false, "No session for this event yet; press enter to start one")
		}
	case "o":
		return m.open(ev.URL)
	case "x":
		m.st.Dismissed[ev.Key] = time.Now()
		delete(m.fresh, ev.Key)
		m.dirty = true
		m.rebuildFeed()
		m.setStatus(false, "Dismissed %s #%d until it changes", ev.Repo, ev.Number)
	}
	return nil
}

func (m *Model) open(url string) tea.Cmd {
	if url == "" {
		return nil
	}
	browse := m.Browse
	return func() tea.Msg { return openedMsg{err: browse(url)} }
}

func (m *Model) jumpToSession(id int) {
	for i, s := range m.sessions {
		if s.ID == id {
			m.sessCursor = i
			m.tab = tabSessions
			m.follow = true
			m.updateTranscript()
			return
		}
	}
}

func (m *Model) eventsView(h int) string {
	if len(m.cfg.Repos) == 0 && len(m.feed) == 0 {
		return "\n  " + sBold.Render("Nothing is being watched yet.") + "\n\n  " +
			sMuted.Render("Press 3 to open Repos, then space to watch a repository.")
	}
	var b strings.Builder
	if errs := len(m.pollErrs); errs > 0 {
		keys := sortedKeys(m.pollErrs)
		b.WriteString(sBad.Render(fit(fmt.Sprintf("⚠ %d repo(s) failed to sync: %s", errs, m.pollErrs[keys[0]]), m.width)))
		b.WriteString("\n")
		h--
	}
	if len(m.feed) == 0 {
		if m.polling > 0 {
			b.WriteString("\n  " + m.spinner.View() + " Checking watched repositories…")
		} else {
			b.WriteString("\n  " + sGood.Render("All quiet.") + sMuted.Render(" No open activity on watched repositories."))
		}
		return b.String()
	}
	top := "show: " + choices(eventFilterNames, int(m.eventFilter)) + "   sort: " + choices(eventSortNames, int(m.eventSort))
	if m.filtering || m.eventQuery.Value() != "" {
		top += "   " + m.eventQuery.View()
	}
	b.WriteString(top)
	b.WriteString("\n")
	h--
	if len(m.shown) == 0 {
		b.WriteString("\n  " + sMuted.Render("Nothing matches. Press f to change the filter or esc to clear the search."))
		return b.String()
	}

	const (
		wKind  = 13
		wNum   = 6
		wAge   = 5
		wState = 13
	)
	wRepo := clamp(m.width/5, 12, 30)
	wTitle := m.width - wKind - wNum - wAge - wState - wRepo - 7
	b.WriteString(sMuted.Render(fit("  "+fit("KIND", wKind)+" "+fit("REPO", wRepo)+" "+fit("#", wNum)+" "+
		fit("TITLE", wTitle)+" "+fit("AGE", wAge)+" "+"CLAUDE", m.width)))
	b.WriteString("\n")
	// Lines are event indexes into m.shown, or -1-section for a header.
	var lines []int
	cur := 0
	for i, ev := range m.shown {
		if m.eventFilter == showFocus && (i == 0 || m.section(ev) != m.section(m.shown[i-1])) {
			lines = append(lines, -1-m.section(ev))
		}
		if i == m.eventCursor {
			cur = len(lines)
		}
		lines = append(lines, i)
	}
	rows := h - 1
	start := window(cur, rows, len(lines))
	for _, i := range lines[start:min(len(lines), start+rows)] {
		if i < 0 {
			b.WriteString(sBold.Render(fit("  "+sectionNames[-1-i], m.width)))
			b.WriteString("\n")
			continue
		}
		ev := m.shown[i]
		marker := "  "
		if m.fresh[ev.Key] {
			marker = sAlert.Render("● ")
		}
		kind := ev.Kind.Short()
		if ev.Severity != "" {
			kind += " " + ev.Severity[:1]
		}
		title := ev.Title
		if ev.Draft {
			title = "[draft] " + title
		}
		if c, ok := m.st.LastChange[ev.Key]; ok && m.fresh[ev.Key] && c.Trigger != config.TriggerOpened {
			title = sAlert.Render("["+c.Trigger.Short()+"]") + " " + title
		}
		sess := ""
		if s, ok := m.sessionFor(ev.Key); ok {
			sess = statusStyle(s.Status).Render(statusIcon(s.Status) + " " + string(s.Status))
		}
		kindCell := kindStyle(ev.Kind).Render(fit(kind, wKind))
		if ev.Severity != "" {
			kindCell = severityStyle(ev.Severity).Render(fit(kind, wKind))
		}
		row := marker + kindCell + " " + fit(ev.Repo, wRepo) + " " + fit(fmt.Sprintf("#%d", ev.Number), wNum) + " " +
			fit(title, wTitle) + " " + sMuted.Render(fit(ago(ev.UpdatedAt), wAge)) + " " + sess
		if i == m.eventCursor {
			row = selectRow(row, m.width)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}

// choices renders names with the selected one highlighted.
func choices(names []string, selected int) string {
	parts := make([]string, len(names))
	for i, n := range names {
		if i == selected {
			parts[i] = sTabOn.Render(n)
		} else {
			parts[i] = sTabOff.Render(n)
		}
	}
	return strings.Join(parts, " ")
}
