package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/drlholloway/fast-lane/internal/claude"
)

const sessionListWidth = 44

func (m *Model) selectedSession() (claude.Session, bool) {
	if m.sessCursor < 0 || m.sessCursor >= len(m.sessions) {
		return claude.Session{}, false
	}
	return m.sessions[m.sessCursor], true
}

func (m *Model) transcriptSize() (int, int) {
	w := m.width - sessionListWidth - 3
	if m.width < 90 {
		// Narrow terminals stack the list above the transcript.
		w = m.width
	}
	h := m.bodyHeight() - 2
	if m.width < 90 {
		h = m.bodyHeight() - m.narrowListHeight() - 3
	}
	return max(w, 10), max(h, 3)
}

func (m *Model) narrowListHeight() int {
	return clamp(len(m.sessions), 1, 6)
}

func (m *Model) sessionsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
		var cmd tea.Cmd
		m.transcript, cmd = m.transcript.Update(msg)
		m.follow = m.transcript.AtBottom()
		return cmd
	case "f":
		m.follow = true
		m.transcript.GotoBottom()
		return nil
	}
	prev := m.sessCursor
	if moveCursor(msg, &m.sessCursor, len(m.sessions), 5) {
		if prev != m.sessCursor {
			m.follow = true
			m.updateTranscript()
		}
		return nil
	}
	s, ok := m.selectedSession()
	if !ok {
		return nil
	}
	switch msg.String() {
	case "enter", "r", "a":
		if len(s.Pending) > 0 {
			delete(m.deferred, s.Pending[0].RequestID)
			m.openRequest(s, s.Pending[0])
			return nil
		}
		if s.Status.Active() || s.Status == claude.StatusQueued {
			m.setStatus(false, "Claude is still working on this; you can reply once it finishes its turn")
			return nil
		}
		if s.ClaudeID == "" {
			m.setStatus(true, "This session never started, so it cannot be resumed")
			return nil
		}
		m.dialog = newReplyDialog(s, m.width, m.height)
	case "x":
		if !s.Status.Active() && s.Status != claude.StatusQueued {
			return nil
		}
		id := s.ID
		m.dialog = newConfirmDialog(fmt.Sprintf("Stop session #%d on %s?", id, s.Event.Repo), func(m *Model) tea.Cmd {
			m.mgr.Stop(id)
			return nil
		}, m.width)
	case "D":
		if s.Status.Active() || s.Status == claude.StatusQueued {
			m.setStatus(false, "Stop the session first (x)")
			return nil
		}
		id := s.ID
		msg := fmt.Sprintf("Delete session #%d on %s and its transcript?", id, s.Event.Repo)
		if s.Checkout != "" {
			msg += "\nIts worktree is removed too; uncommitted changes in it are lost, committed branches stay in " + tildify(s.Checkout) + "."
		}
		m.dialog = newConfirmDialog(msg, func(m *Model) tea.Cmd {
			err := m.mgr.Delete(id)
			for k, v := range m.st.EventSessions {
				if v == id {
					delete(m.st.EventSessions, k)
				}
			}
			delete(m.pending, id)
			m.refreshSessions()
			if err != nil {
				m.setStatus(true, "Deleted session #%d, but: %v", id, err)
			} else {
				m.setStatus(false, "Deleted session #%d", id)
			}
			return nil
		}, m.width)
	case "o":
		return m.open(s.Event.URL)
	}
	return nil
}

// sessionDir says where s runs, or will run once it starts.
func (m *Model) sessionDir(s claude.Session) string {
	if s.Dir == "" {
		return m.location(s.Event.Repo)
	}
	if s.Checkout != "" {
		return tildify(s.Dir) + " (worktree of " + tildify(s.Checkout) + ")"
	}
	return tildify(s.Dir)
}

func (m *Model) updateTranscript() {
	w, h := m.transcriptSize()
	m.transcript.Width, m.transcript.Height = w, h
	s, ok := m.selectedSession()
	if !ok {
		m.transcript.SetContent("")
		return
	}
	m.transcript.SetContent(renderTranscript(s, w))
	if m.follow {
		m.transcript.GotoBottom()
	}
}

func renderTranscript(s claude.Session, w int) string {
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	for _, l := range s.Lines {
		var line string
		switch l.Kind {
		case claude.LineText:
			line = wrap.Render(l.Text)
		case claude.LineTool:
			line = sMuted.Render(fit("  → "+l.Text, w))
		case claude.LineToolError:
			line = sBad.Render(fit("  ✗ "+l.Text, w))
		case claude.LineUser:
			line = sKey.Render(wrap.Render("▶ you: " + l.Text))
		case claude.LineInfo:
			line = sMuted.Render(wrap.Render("· " + l.Text))
		case claude.LineError:
			line = sBad.Render(wrap.Render("✗ " + l.Text))
		case claude.LineDone:
			line = sGood.Render("✓ " + l.Text)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(s.Pending) > 0 {
		b.WriteString(sAlert.Render(fmt.Sprintf("! waiting on you: %s — press enter", s.Pending[0].ToolName)))
	}
	return b.String()
}

func (m *Model) sessionsView(h int) string {
	if len(m.sessions) == 0 {
		return "\n  " + sBold.Render("No Claude sessions yet.") + "\n\n  " +
			sMuted.Render("Pick an event on the Events tab and press enter, or turn on auto-run for a repo.")
	}
	narrow := m.width < 90
	listW := sessionListWidth
	listH := h
	if narrow {
		listW = m.width
		listH = m.narrowListHeight()
	}
	var list strings.Builder
	start := window(m.sessCursor, listH, len(m.sessions))
	for i := start; i < len(m.sessions) && i < start+listH; i++ {
		s := m.sessions[i]
		icon := statusStyle(s.Status).Render(statusIcon(s.Status))
		label := fmt.Sprintf("#%d %s %s#%d", s.ID, s.Event.Kind.Short(), shortRepo(s.Event.Repo), s.Event.Number)
		cost := ""
		if s.CostUSD > 0 {
			cost = fmt.Sprintf("$%.2f", s.CostUSD)
		}
		row := icon + " " + fit(label, listW-20) + " " + statusStyle(s.Status).Render(fit(string(s.Status), 9)) + " " + sMuted.Render(fit(cost, 8))
		if i == m.sessCursor {
			row = selectRow(row, listW)
		}
		list.WriteString(row)
		list.WriteString("\n")
	}

	s, _ := m.selectedSession()
	head := sBold.Render(fit(s.Event.Title, m.transcript.Width))
	started := ago(s.Started)
	if started != "now" {
		started += " ago"
	}
	sub := sMuted.Render(fit(fmt.Sprintf("%s · %s #%d · started %s · %s", s.Event.Repo, s.Event.Kind.Short(),
		s.Event.Number, started, m.sessionDir(s)), m.transcript.Width))
	right := head + "\n" + sub + "\n" + m.transcript.View()

	if narrow {
		return list.String() + sMuted.Render(strings.Repeat("─", m.width)) + "\n" + right
	}
	sep := sMuted.Render(strings.Repeat("│\n", h))
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(listW).Render(list.String()), " ", sep, " ", right)
}

func shortRepo(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}
