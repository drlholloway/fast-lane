package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// dialog is a modal that takes over the body and all key input.
// update returns nil when the dialog closes.
type dialog interface {
	update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd)
	view(m *Model) string
	help() string
	resize(w, h int)
	passthrough(msg tea.Msg) tea.Cmd
}

func dialogWidth(w int) int { return clamp(w-6, 30, 96) }

// wrapLimit wraps s to w cells and keeps at most n lines.
func wrapLimit(s string, w, n int) string {
	lines := strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n-1], sMuted.Render(fmt.Sprintf("… %d more lines", len(lines)-n+1)))
	}
	return strings.Join(lines, "\n")
}

func sessionTitle(s claude.Session) string {
	return fmt.Sprintf("Session #%d · %s %s #%d", s.ID, s.Event.Repo, s.Event.Kind.Short(), s.Event.Number)
}

// ---- permission ----

type permissionDialog struct {
	s       claude.Session
	r       claude.Request
	reason  textinput.Model
	denying bool
	width   int
	height  int
}

func newPermissionDialog(s claude.Session, r claude.Request, w, h int) *permissionDialog {
	ti := textinput.New()
	ti.Placeholder = "tell Claude why, or what to do instead (optional)"
	ti.Prompt = "> "
	return &permissionDialog{s: s, r: r, reason: ti, width: dialogWidth(w), height: h}
}

func (d *permissionDialog) canAlways() bool {
	return len(d.r.Suggestions) > 0 && string(d.r.Suggestions) != "null" && string(d.r.Suggestions) != "[]"
}

func (d *permissionDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	if d.denying {
		switch msg.String() {
		case "esc":
			d.denying = false
			d.reason.Blur()
			return d, nil
		case "enter":
			m.respond(d.r, claude.Answer{Allow: false, Message: strings.TrimSpace(d.reason.Value())})
			return nil, nil
		}
		var cmd tea.Cmd
		d.reason, cmd = d.reason.Update(msg)
		return d, cmd
	}
	switch msg.String() {
	case "y", "enter":
		m.respond(d.r, claude.Answer{Allow: true})
		return nil, nil
	case "a":
		if d.canAlways() {
			m.respond(d.r, claude.Answer{Allow: true, Always: true})
			return nil, nil
		}
	case "n":
		d.denying = true
		return d, d.reason.Focus()
	case "esc":
		m.deferred[d.r.RequestID] = true
		m.setStatus(false, "Request deferred; answer it from the Sessions tab")
		return nil, nil
	case "s":
		m.deferred[d.r.RequestID] = true
		m.jumpToSession(d.s.ID)
		return nil, nil
	}
	return d, nil
}

func (d *permissionDialog) view(m *Model) string {
	w := d.width - 6
	var b strings.Builder
	b.WriteString(sAlert.Render("Claude needs permission") + "\n")
	b.WriteString(sMuted.Render(fit(sessionTitle(d.s), w)) + "\n")
	b.WriteString(sMuted.Render(fit(d.s.Event.Title, w)) + "\n\n")
	b.WriteString(sBold.Render(d.r.ToolName))
	if d.r.Description != "" && d.r.Description != d.r.Summary {
		b.WriteString(sMuted.Render(" — " + d.r.Description))
	}
	b.WriteString("\n")
	if d.r.Summary != "" {
		box := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colMuted).Padding(0, 1)
		b.WriteString(box.Render(wrapLimit(d.r.Summary, w-4, clamp(d.height-16, 3, 20))) + "\n")
	}
	b.WriteString("\n")
	if d.denying {
		b.WriteString("Deny — " + d.reason.View())
	} else {
		opts := []string{sKey.Render("y") + " allow"}
		if d.canAlways() {
			opts = append(opts, sKey.Render("a")+" allow for this session")
		}
		opts = append(opts, sKey.Render("n")+" deny", sKey.Render("s")+" view session", sKey.Render("esc")+" later")
		b.WriteString(strings.Join(opts, "   "))
	}
	return sDialog.Width(d.width).Render(b.String())
}

func (d *permissionDialog) help() string {
	if d.denying {
		return helpLine("enter", "deny", "esc", "back")
	}
	return helpLine("y", "allow", "n", "deny", "esc", "later")
}

func (d *permissionDialog) resize(w, h int)                 { d.width, d.height = dialogWidth(w), h }
func (d *permissionDialog) passthrough(msg tea.Msg) tea.Cmd { return nil }

func (m *Model) respond(r claude.Request, a claude.Answer) {
	if err := m.mgr.Respond(r, a); err != nil {
		m.setStatus(true, "Could not answer Claude: %v", err)
	}
	delete(m.deferred, r.RequestID)
	m.refreshSessions()
}

// ---- AskUserQuestion ----

type questionDialog struct {
	s       claude.Session
	r       claude.Request
	qi      int
	cursor  int
	picked  map[int]bool
	answers map[string]string
	other   textinput.Model
	typing  bool
	width   int
}

func newQuestionDialog(s claude.Session, r claude.Request, w, h int) *questionDialog {
	ti := textinput.New()
	ti.Placeholder = "type your answer"
	ti.Prompt = "> "
	return &questionDialog{s: s, r: r, picked: map[int]bool{}, answers: map[string]string{}, other: ti, width: dialogWidth(w)}
}

func (d *questionDialog) q() claude.Question { return d.r.Questions[d.qi] }

// otherIndex is the cursor position of the free-text choice.
func (d *questionDialog) otherIndex() int { return len(d.q().Options) }

func (d *questionDialog) next(m *Model, answer string) (dialog, tea.Cmd) {
	d.answers[d.q().Question] = answer
	if d.qi+1 < len(d.r.Questions) {
		d.qi++
		d.cursor = 0
		d.picked = map[int]bool{}
		d.other.SetValue("")
		return d, nil
	}
	m.respond(d.r, claude.Answer{Allow: true, Answers: d.answers})
	return nil, nil
}

func (d *questionDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	q := d.q()
	if d.typing {
		switch msg.String() {
		case "esc":
			d.typing = false
			d.other.Blur()
			return d, nil
		case "enter":
			text := strings.TrimSpace(d.other.Value())
			if text == "" {
				return d, nil
			}
			d.typing = false
			d.other.Blur()
			if q.MultiSelect {
				return d.next(m, strings.Join(append(d.pickedLabels(), text), ", "))
			}
			return d.next(m, text)
		}
		var cmd tea.Cmd
		d.other, cmd = d.other.Update(msg)
		return d, cmd
	}
	switch msg.String() {
	case "up", "k":
		d.cursor = clamp(d.cursor-1, 0, d.otherIndex())
	case "down", "j":
		d.cursor = clamp(d.cursor+1, 0, d.otherIndex())
	case " ":
		if q.MultiSelect && d.cursor < d.otherIndex() {
			d.picked[d.cursor] = !d.picked[d.cursor]
		}
	case "enter":
		if d.cursor == d.otherIndex() {
			d.typing = true
			return d, d.other.Focus()
		}
		if q.MultiSelect {
			labels := d.pickedLabels()
			if len(labels) == 0 {
				labels = []string{q.Options[d.cursor].Label}
			}
			return d.next(m, strings.Join(labels, ", "))
		}
		return d.next(m, q.Options[d.cursor].Label)
	case "d":
		m.respond(d.r, claude.Answer{Allow: false, Message: "The user chose not to answer. Use your best judgment or stop and summarize."})
		return nil, nil
	case "esc":
		m.deferred[d.r.RequestID] = true
		m.setStatus(false, "Question deferred; answer it from the Sessions tab")
		return nil, nil
	default:
		if k := msg.String(); len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < len(q.Options) {
			d.cursor = int(k[0] - '1')
		}
	}
	return d, nil
}

func (d *questionDialog) pickedLabels() []string {
	var out []string
	for i, o := range d.q().Options {
		if d.picked[i] {
			out = append(out, o.Label)
		}
	}
	return out
}

func (d *questionDialog) view(m *Model) string {
	w := d.width - 6
	q := d.q()
	var b strings.Builder
	head := "Claude has a question"
	if len(d.r.Questions) > 1 {
		head += fmt.Sprintf(" (%d of %d)", d.qi+1, len(d.r.Questions))
	}
	b.WriteString(sAlert.Render(head) + "\n")
	b.WriteString(sMuted.Render(fit(sessionTitle(d.s), w)) + "\n\n")
	b.WriteString(sBold.Render(wrapLimit(q.Question, w, 6)) + "\n")
	if q.MultiSelect {
		b.WriteString(sMuted.Render("Pick any that apply.") + "\n")
	}
	b.WriteString("\n")
	for i, o := range q.Options {
		mark := "  "
		if q.MultiSelect {
			mark = "[ ] "
			if d.picked[i] {
				mark = sGood.Render("[✓] ")
			}
		}
		line := fmt.Sprintf("%d. %s%s", i+1, mark, o.Label)
		if i == d.cursor {
			line = sKey.Render("› ") + sBold.Render(line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
		if o.Description != "" {
			b.WriteString(sMuted.Render(wrapLimit("     "+o.Description, w, 3)) + "\n")
		}
	}
	other := "Something else…"
	if d.cursor == d.otherIndex() {
		b.WriteString(sKey.Render("› ") + sBold.Render(other) + "\n")
	} else {
		b.WriteString("  " + other + "\n")
	}
	if d.typing {
		b.WriteString("\n" + d.other.View())
	}
	return sDialog.Width(d.width).Render(b.String())
}

func (d *questionDialog) help() string {
	if d.typing {
		return helpLine("enter", "send", "esc", "back")
	}
	if d.q().MultiSelect {
		return helpLine("space", "toggle", "enter", "confirm", "d", "decline", "esc", "later")
	}
	return helpLine("↑/↓", "choose", "enter", "answer", "d", "decline", "esc", "later")
}

func (d *questionDialog) resize(w, h int)                 { d.width = dialogWidth(w) }
func (d *questionDialog) passthrough(msg tea.Msg) tea.Cmd { return nil }

// ---- free-form text (reply / start) ----

func newTextarea(w, h int, value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.SetWidth(w)
	ta.SetHeight(h)
	ta.SetValue(value)
	ta.Focus()
	return ta
}

type replyDialog struct {
	s     claude.Session
	ta    textarea.Model
	width int
}

func newReplyDialog(s claude.Session, w, h int) *replyDialog {
	dw := dialogWidth(w)
	return &replyDialog{s: s, ta: newTextarea(dw-6, clamp(h/4, 3, 10), ""), width: dw}
}

func (d *replyDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		text := strings.TrimSpace(d.ta.Value())
		if text == "" {
			return d, nil
		}
		if err := m.mgr.Reply(d.s.ID, text); err != nil {
			m.setStatus(true, "%v", err)
		}
		m.follow = true
		m.refreshSessions()
		return nil, nil
	}
	var cmd tea.Cmd
	d.ta, cmd = d.ta.Update(msg)
	return d, cmd
}

func (d *replyDialog) view(m *Model) string {
	w := d.width - 6
	var b strings.Builder
	b.WriteString(sTitle.Render("Reply to Claude") + "\n")
	b.WriteString(sMuted.Render(fit(sessionTitle(d.s), w)) + "\n\n")
	if d.s.Result != "" {
		b.WriteString(sMuted.Render("Claude said:") + "\n")
		b.WriteString(wrapLimit(d.s.Result, w, 10) + "\n\n")
	}
	b.WriteString(d.ta.View())
	return sDialog.Width(d.width).Render(b.String())
}

func (d *replyDialog) help() string {
	return helpLine("enter", "send", "alt+enter", "newline", "esc", "cancel")
}

func (d *replyDialog) resize(w, h int) {
	d.width = dialogWidth(w)
	d.ta.SetWidth(d.width - 6)
}

func (d *replyDialog) passthrough(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.ta, cmd = d.ta.Update(msg)
	return cmd
}

type startDialog struct {
	ev      github.Event
	allow   []config.Action
	workdir string
	ta      textarea.Model
	width   int
}

func newStartDialog(ev github.Event, text string, allow []config.Action, workdir string, w, h int) *startDialog {
	dw := dialogWidth(w)
	return &startDialog{ev: ev, allow: allow, workdir: workdir, ta: newTextarea(dw-6, clamp(h-14, 5, 24), text), width: dw}
}

func (d *startDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "ctrl+s":
		text := strings.TrimSpace(d.ta.Value())
		if text == "" {
			return d, nil
		}
		if err := m.startSessionWithPrompt(d.ev, text, d.allow); err != nil {
			m.setStatus(true, "Could not start Claude: %v", err)
			return nil, nil
		}
		m.setStatus(false, "Started Claude on %s #%d", d.ev.Repo, d.ev.Number)
		if s, ok := m.sessionFor(d.ev.Key); ok {
			m.jumpToSession(s.ID)
		}
		return nil, nil
	}
	var cmd tea.Cmd
	d.ta, cmd = d.ta.Update(msg)
	return d, cmd
}

func (d *startDialog) view(m *Model) string {
	w := d.width - 6
	var b strings.Builder
	b.WriteString(sTitle.Render("Start a Claude session") + "\n")
	b.WriteString(sMuted.Render(fit(fmt.Sprintf("%s %s #%d · runs in %s", d.ev.Repo, d.ev.Kind.Short(), d.ev.Number, d.workdir), w)) + "\n")
	if len(d.allow) > 0 {
		names := make([]string, len(d.allow))
		for i, a := range d.allow {
			names[i] = string(a)
		}
		b.WriteString(sWarn.Render(fit("Allowed without asking: "+strings.Join(names, ", "), w)) + "\n")
	}
	b.WriteString(sMuted.Render("Edit the instructions if you like, then press ctrl+s.") + "\n\n")
	b.WriteString(d.ta.View())
	return sDialog.Width(d.width).Render(b.String())
}

func (d *startDialog) help() string {
	return helpLine("ctrl+s", "start", "esc", "cancel")
}

func (d *startDialog) resize(w, h int) {
	d.width = dialogWidth(w)
	d.ta.SetWidth(d.width - 6)
}

func (d *startDialog) passthrough(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.ta, cmd = d.ta.Update(msg)
	return cmd
}

// ---- confirm ----

type confirmDialog struct {
	msg   string
	onYes func(*Model) tea.Cmd
	width int
}

func newConfirmDialog(msg string, onYes func(*Model) tea.Cmd, w int) *confirmDialog {
	return &confirmDialog{msg: msg, onYes: onYes, width: clamp(w-6, 30, 64)}
}

func (d *confirmDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		return nil, d.onYes(m)
	case "n", "esc", "q":
		return nil, nil
	}
	return d, nil
}

func (d *confirmDialog) view(m *Model) string {
	return sDialog.Width(d.width).Render(d.msg + "\n\n" + sKey.Render("y") + " yes   " + sKey.Render("n") + " no")
}

func (d *confirmDialog) help() string                    { return helpLine("y", "yes", "n", "no") }
func (d *confirmDialog) resize(w, h int)                 { d.width = clamp(w-6, 30, 64) }
func (d *confirmDialog) passthrough(msg tea.Msg) tea.Cmd { return nil }

// ---- per-repo settings ----

type kindsDialog struct {
	repo    string
	enabled map[config.EventKind]bool
	path    textinput.Model
	cursor  int
	editing bool
	width   int
}

func newKindsDialog(r config.Repo, w int) *kindsDialog {
	en := map[config.EventKind]bool{}
	for _, k := range r.Events {
		en[k] = true
	}
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "(clone into workspace)"
	ti.SetValue(r.LocalPath)
	return &kindsDialog{repo: r.FullName, enabled: en, path: ti, width: clamp(w-6, 30, 72)}
}

func (d *kindsDialog) pathRow() int { return len(config.AllKinds) }

func (d *kindsDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	if d.editing {
		switch msg.String() {
		case "enter", "esc":
			d.editing = false
			d.path.Blur()
			return d, nil
		}
		var cmd tea.Cmd
		d.path, cmd = d.path.Update(msg)
		return d, cmd
	}
	switch msg.String() {
	case "up", "k":
		d.cursor = clamp(d.cursor-1, 0, d.pathRow())
	case "down", "j":
		d.cursor = clamp(d.cursor+1, 0, d.pathRow())
	case " ", "enter":
		if d.cursor == d.pathRow() {
			d.editing = true
			return d, d.path.Focus()
		}
		k := config.AllKinds[d.cursor]
		d.enabled[k] = !d.enabled[k]
	case "ctrl+s", "s":
		rc := m.cfg.Repo(d.repo)
		if rc == nil {
			return nil, nil
		}
		rc.Events = rc.Events[:0]
		for _, k := range config.AllKinds {
			if d.enabled[k] {
				rc.Events = append(rc.Events, k)
			}
		}
		rc.LocalPath = strings.TrimSpace(d.path.Value())
		m.rebuildFeed()
		return nil, tea.Batch(m.saveConfig("Saved settings for %s", d.repo), m.pollRepo(*rc))
	case "esc":
		return nil, nil
	}
	return d, nil
}

func (d *kindsDialog) view(m *Model) string {
	var b strings.Builder
	b.WriteString(sTitle.Render("Watch settings") + "\n" + sMuted.Render(d.repo) + "\n\n")
	for i, k := range config.AllKinds {
		box := "[ ]"
		if d.enabled[k] {
			box = sGood.Render("[✓]")
		}
		line := box + " " + kindLabel(k)
		if i == d.cursor {
			line = sKey.Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	pathLine := "Local checkout: " + d.path.View()
	if d.cursor == d.pathRow() {
		pathLine = sKey.Render("› ") + pathLine
	} else {
		pathLine = "  " + pathLine
	}
	b.WriteString("\n" + pathLine + "\n")
	b.WriteString(sMuted.Render("  Claude runs here. Leave empty to clone into the workspace directory.") + "\n")
	return sDialog.Width(d.width).Render(b.String())
}

func kindLabel(k config.EventKind) string {
	switch k {
	case config.KindPullRequest:
		return "Pull requests"
	case config.KindIssue:
		return "Issues"
	case config.KindDependabot:
		return "Dependabot alerts"
	case config.KindCodeScanning:
		return "Code scanning alerts"
	}
	return string(k)
}

func (d *kindsDialog) help() string {
	if d.editing {
		return helpLine("enter", "done")
	}
	return helpLine("space", "toggle", "s", "save", "esc", "cancel")
}

func (d *kindsDialog) resize(w, h int)                 { d.width = clamp(w-6, 30, 72) }
func (d *kindsDialog) passthrough(msg tea.Msg) tea.Cmd { return nil }
