package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/prompt"
)

func (m *Model) selectedEvent() (github.Event, bool) {
	if m.eventCursor < 0 || m.eventCursor >= len(m.feed) {
		return github.Event{}, false
	}
	return m.feed[m.eventCursor], true
}

func (m *Model) eventsKey(msg tea.KeyMsg) tea.Cmd {
	if moveCursor(msg, &m.eventCursor, len(m.feed), m.bodyHeight()-2) {
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
		m.dialog = newStartDialog(ev, text, rule.Allow, m.cfg.WorkDir(ev.Repo), m.width, m.height)
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
	if len(m.cfg.Repos) == 0 {
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
	rows := h - 1
	start := window(m.eventCursor, rows, len(m.feed))
	for i := start; i < len(m.feed) && i < start+rows; i++ {
		ev := m.feed[i]
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
