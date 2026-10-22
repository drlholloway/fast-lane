package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/doctor"
	"github.com/drlholloway/fast-lane/internal/local"
	"github.com/drlholloway/fast-lane/internal/prompt"
)

// The Settings tab checks prerequisites and edits every top-level setting.
// Edits save to the config file right away.

type checksMsg []doctor.Check

func (m *Model) runChecks() tea.Cmd {
	m.checking = true
	env, check := doctor.Env{Config: m.cfg.Clone(), GitHub: m.gh}, m.Check
	return func() tea.Msg { return checksMsg(check(context.Background(), env)) }
}

func (m *Model) onChecks(cs checksMsg) {
	first := m.checks == nil
	m.checks, m.checking = cs, false
	if !first {
		return
	}
	var bad []string
	for _, c := range cs {
		if c.Status != doctor.OK {
			bad = append(bad, c.Name)
		}
	}
	if len(bad) > 0 {
		m.setStatus(doctor.Worst(cs) == doctor.Fail, "Setup needs attention (%s): see Settings (5)", strings.Join(bad, ", "))
	}
}

// setting is one row of the Settings tab.
type setting struct {
	// group is the heading the row sits under.
	group string
	label string
	about string
	value func(m *Model) string
	// edit opens a dialog or changes the setting in place.
	edit func(m *Model) tea.Cmd
}

var permissionModes = []string{"manual", "acceptEdits", "auto"}

const (
	repoURL   = "https://github.com/drlholloway/fast-lane"
	coffeeURL = "https://buymeacoffee.com/drlholloway"
)

func stripScheme(u string) string { return strings.TrimPrefix(u, "https://") }

func lines(v []string) string { return strings.Join(v, "\n") }

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func orNone(s, none string) string {
	if s == "" {
		return sMuted.Render(none)
	}
	return s
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// positive parses a whole number of at least 1.
func positive(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("enter a whole number of at least 1")
	}
	return n, nil
}

var settings = []setting{
	{label: "Poll interval", about: "How often watched repos and the inbox are checked. At least 30s.",
		value: func(m *Model) string { return m.cfg.PollInterval.String() },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Poll interval", "A duration such as 2m or 90s.", m.cfg.PollInterval.String(), false,
				func(m *Model, v string) (tea.Cmd, error) {
					d, err := time.ParseDuration(strings.TrimSpace(v))
					if err != nil || d < 30*time.Second {
						return nil, fmt.Errorf("enter a duration of at least 30s, such as 2m")
					}
					m.cfg.PollInterval.Duration = d
					return m.saveConfig("Polling every %s from the next poll", d), nil
				})
		}},
	{label: "Max concurrent sessions", about: "How many Claude sessions may run at once.",
		value: func(m *Model) string { return strconv.Itoa(m.cfg.MaxConcurrent) },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Max concurrent sessions", "", strconv.Itoa(m.cfg.MaxConcurrent), false,
				func(m *Model, v string) (tea.Cmd, error) {
					n, err := positive(v)
					if err != nil {
						return nil, err
					}
					m.cfg.MaxConcurrent = n
					return m.saveConfig("Up to %d sessions at once", n), nil
				})
		}},
	{label: "Max auto-runs per item", about: "How many times auto-run may act on one PR or issue, so nothing loops forever.",
		value: func(m *Model) string { return strconv.Itoa(m.cfg.MaxAutoRuns) },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Max auto-runs per item", "", strconv.Itoa(m.cfg.MaxAutoRuns), false,
				func(m *Model, v string) (tea.Cmd, error) {
					n, err := positive(v)
					if err != nil {
						return nil, err
					}
					m.cfg.MaxAutoRuns = n
					return m.saveConfig("Auto-run acts on an item at most %d times", n), nil
				})
		}},
	{label: "Workspace", about: "Where repos without a local checkout are cloned, and where session worktrees live.",
		value: func(m *Model) string { return tildify(m.cfg.WorkspaceDir) },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Workspace", "A folder Fast Lane can write to. ~ is your home folder.", tildify(m.cfg.WorkspaceDir), false,
				func(m *Model, v string) (tea.Cmd, error) {
					v = strings.TrimSpace(v)
					if v == "" {
						return nil, fmt.Errorf("enter a folder")
					}
					m.cfg.WorkspaceDir = config.ExpandHome(v)
					return tea.Batch(m.saveConfig("Workspace set to %s for new sessions", v), m.runChecks()), nil
				})
		}},
	{label: "Code folders", about: "Searched for local checkouts, which sessions use through git worktrees instead of cloning.",
		value: func(m *Model) string {
			return orNone(strings.Join(m.cfg.CodeDirs, ", "), "none: always clone")
		},
		edit: func(m *Model) tea.Cmd {
			return m.edit("Code folders", "One folder per line, searched three levels deep. Leave empty to always clone.",
				lines(m.cfg.CodeDirs), true, func(m *Model, v string) (tea.Cmd, error) {
					m.cfg.CodeDirs = splitLines(v)
					found := local.Scan(m.cfg.SearchDirs())
					m.mgr.SetCheckouts(found)
					return tea.Batch(m.saveConfig("Found %s in the code folders", count(len(found), "checkout", "checkouts")),
						m.runChecks()), nil
				})
		}},
	{label: "Claude path", about: "The claude executable, by name on your PATH or as a full path.",
		value: func(m *Model) string { return m.cfg.Claude.Path },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Claude path", "", m.cfg.Claude.Path, false, func(m *Model, v string) (tea.Cmd, error) {
				if v = strings.TrimSpace(v); v == "" {
					return nil, fmt.Errorf("enter claude or a full path")
				}
				m.cfg.Claude.Path = config.ExpandHome(v)
				return tea.Batch(m.saveConfig("Claude path set to %s", v), m.runChecks()), nil
			})
		}},
	{label: "Model", about: "Passed to claude --model. Empty uses Claude Code's default.",
		value: func(m *Model) string { return orNone(m.cfg.Claude.Model, "Claude Code's default") },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Model", "A model name or alias Claude Code accepts, such as opus or sonnet. Empty for the default.",
				m.cfg.Claude.Model, false, func(m *Model, v string) (tea.Cmd, error) {
					m.cfg.Claude.Model = strings.TrimSpace(v)
					return m.saveConfig("Model saved for new turns"), nil
				})
		}},
	{label: "Effort", about: "How hard Claude thinks, passed to claude --effort. enter cycles Claude Code's default, low, medium, high, xhigh, max.",
		value: func(m *Model) string { return orNone(m.cfg.Claude.Effort, "Claude Code's default") },
		edit: func(m *Model) tea.Cmd {
			m.cfg.Claude.Effort = cycle(append([]string{""}, config.Efforts...), m.cfg.Claude.Effort, 1)
			if m.cfg.Claude.Effort == "" {
				return m.saveConfig("Effort: Claude Code's default for new turns")
			}
			return m.saveConfig("Effort %s for new turns", m.cfg.Claude.Effort)
		}},
	{label: "Permission mode", about: "How Claude Code settles permission prompts on its own; the rest come to you. enter cycles manual, acceptEdits, auto.",
		value: func(m *Model) string { return m.cfg.Claude.PermissionMode },
		edit: func(m *Model) tea.Cmd {
			m.cfg.Claude.PermissionMode = cycle(permissionModes, m.cfg.Claude.PermissionMode, 1)
			return m.saveConfig("Permission mode %s for new turns", m.cfg.Claude.PermissionMode)
		}},
	{label: "Max budget per session", about: "Caps what one session may spend, in US dollars. Empty for no cap.",
		value: func(m *Model) string {
			if m.cfg.Claude.MaxBudgetUSD <= 0 {
				return sMuted.Render("no cap")
			}
			return fmt.Sprintf("$%.2f", m.cfg.Claude.MaxBudgetUSD)
		},
		edit: func(m *Model) tea.Cmd {
			cur := ""
			if m.cfg.Claude.MaxBudgetUSD > 0 {
				cur = strconv.FormatFloat(m.cfg.Claude.MaxBudgetUSD, 'f', -1, 64)
			}
			return m.edit("Max budget per session (USD)", "Such as 5 or 2.50. Empty for no cap.", cur, false,
				func(m *Model, v string) (tea.Cmd, error) {
					v = strings.TrimPrefix(strings.TrimSpace(v), "$")
					f := 0.0
					if v != "" {
						var err error
						if f, err = strconv.ParseFloat(v, 64); err != nil || f < 0 {
							return nil, fmt.Errorf("enter an amount such as 5 or 2.50")
						}
					}
					m.cfg.Claude.MaxBudgetUSD = f
					return m.saveConfig("Budget saved for new turns"), nil
				})
		}},
	{label: "Allowed tools", about: "Permission rules Claude never asks about, in every session. Rules' allow lists add to these.",
		value: func(m *Model) string { return count(len(m.cfg.Claude.AllowedTools), "rule", "rules") },
		edit: func(m *Model) tea.Cmd {
			return m.edit("Allowed tools", "One Claude Code permission rule per line, such as Read or Bash(git log:*).",
				lines(m.cfg.Claude.AllowedTools), true, func(m *Model, v string) (tea.Cmd, error) {
					m.cfg.Claude.AllowedTools = splitLines(v)
					return m.saveConfig("Allowed tools saved for new turns"), nil
				})
		}},
	{label: "Extra claude arguments", about: "Appended to every claude command.",
		value: func(m *Model) string {
			return orNone(strings.Join(m.cfg.Claude.ExtraArgs, " "), "none")
		},
		edit: func(m *Model) tea.Cmd {
			return m.edit("Extra claude arguments", "One argument per line.", lines(m.cfg.Claude.ExtraArgs), true,
				func(m *Model, v string) (tea.Cmd, error) {
					m.cfg.Claude.ExtraArgs = splitLines(v)
					return m.saveConfig("Extra arguments saved for new turns"), nil
				})
		}},
	{label: "Inbox", about: "Review requests, mentions and assignments from your notifications. enter cycles list only, off, auto-run, full auto.",
		value: func(m *Model) string { return inboxLabel(m.cfg.Inbox) },
		edit: func(m *Model) tea.Cmd {
			return tea.Batch(m.cycleInbox(), m.runChecks())
		}},
	{label: "Prompts", about: "The default instructions for each kind and trigger. Rules can still set their own.",
		value: func(m *Model) string {
			if len(m.cfg.Prompts) == 0 {
				return sMuted.Render("all built-in")
			}
			return count(len(m.cfg.Prompts), "custom prompt", "custom prompts")
		},
		edit: func(m *Model) tea.Cmd {
			m.dialog = newPromptsDialog(m.width)
			return nil
		}},
	{group: "About", label: "Version", about: "The build of Fast Lane you're running; fast-lane -version prints it too. Fast Lane looks for a newer release when it starts and once a day, and c looks again. enter opens the newest release.",
		value: func(m *Model) string { return m.versionValue() },
		edit:  func(m *Model) tea.Cmd { return m.openRelease() }},
	{group: "About", label: "Source", about: "Code, issues and releases. enter opens it in your browser.",
		value: func(m *Model) string { return stripScheme(repoURL) },
		edit:  func(m *Model) tea.Cmd { return m.open(repoURL) }},
	{group: "About", label: "Buy me a coffee", about: "Fast Lane is free. If it saved you time, a coffee keeps it in the fast lane. enter opens it in your browser.",
		value: func(m *Model) string { return sWarn.Render("☕ ") + stripScheme(coffeeURL) },
		edit:  func(m *Model) tea.Cmd { return m.open(coffeeURL) }},
}

func (m *Model) settingsKey(msg tea.KeyMsg) tea.Cmd {
	if moveCursor(msg, &m.setCursor, len(settings), 5) {
		return nil
	}
	switch msg.String() {
	case "c":
		return tea.Batch(m.runChecks(), m.checkRelease())
	case "enter", " ":
		return settings[m.setCursor].edit(m)
	}
	return nil
}

func statusMark(s doctor.Status) string {
	switch s {
	case doctor.OK:
		return sGood.Render("✓")
	case doctor.Warn:
		return sWarn.Render("!")
	}
	return sBad.Render("✗")
}

func (m *Model) settingsView(h int) string {
	var out []string
	add := func(s string) { out = append(out, s) }

	head := sTitle.Render("Prerequisites") + sMuted.Render("  c checks again")
	if m.checking {
		head += "  " + m.spinner.View() + sMuted.Render(" checking…")
	}
	add(head)
	const wName = 15
	for _, c := range m.checks {
		add(fit(" "+statusMark(c.Status)+" "+fit(c.Name, wName)+" "+tildify(c.Detail), m.width))
		if c.Status != doctor.OK && c.Fix != "" {
			for _, l := range strings.Split(wrapLimit(c.Fix, m.width-wName-6, 3), "\n") {
				add(strings.Repeat(" ", wName+4) + sWarn.Render(l))
			}
		}
	}
	if len(m.checks) == 0 && !m.checking {
		add(sMuted.Render("   not checked yet"))
	}
	add("")
	add(sTitle.Render("Settings") + sMuted.Render("  saved to "+tildify(m.cfg.Path())))
	const wLabel = 25
	cursorLine := 0
	m.setCursor = clamp(m.setCursor, 0, len(settings)-1)
	for i, s := range settings {
		if i > 0 && s.group != settings[i-1].group {
			add("")
			add(sTitle.Render(s.group))
		}
		row := "  " + fit(s.label, wLabel) + " " + s.value(m)
		if i == m.setCursor {
			cursorLine = len(out)
			row = selectRow(row, m.width)
		}
		add(fit(row, m.width))
	}
	add("")
	for _, l := range strings.Split(wrapLimit(settings[m.setCursor].about, m.width-4, 2), "\n") {
		add("  " + sMuted.Render(l))
	}

	// Keep the selected setting in view on short terminals.
	start := 0
	if len(out) > h {
		start = clamp(cursorLine-h+4, 0, len(out)-h)
	}
	return strings.Join(out[start:min(len(out), start+h)], "\n")
}

// ---- edit one value ----

type editDialog struct {
	title, hint string
	multiline   bool
	ti          textinput.Model
	ta          textarea.Model
	save        func(m *Model, v string) (tea.Cmd, error)
	// back is shown again when the dialog closes, if set.
	back  dialog
	err   string
	width int
}

func (m *Model) edit(title, hint, value string, multiline bool, save func(*Model, string) (tea.Cmd, error)) tea.Cmd {
	d := newEditDialog(title, hint, value, multiline, save, m.width, m.height)
	m.dialog = d
	if multiline {
		return textarea.Blink
	}
	return textinput.Blink
}

func newEditDialog(title, hint, value string, multiline bool, save func(*Model, string) (tea.Cmd, error), w, h int) *editDialog {
	d := &editDialog{title: title, hint: hint, multiline: multiline, save: save, width: dialogWidth(w)}
	if multiline {
		d.ta = textarea.New()
		d.ta.ShowLineNumbers = false
		d.ta.Prompt = ""
		d.ta.CharLimit = 0
		d.ta.SetWidth(d.width - 6)
		d.ta.SetHeight(clamp(h-14, 5, 30))
		d.ta.SetValue(value)
		d.ta.Focus()
	} else {
		d.ti = textinput.New()
		d.ti.Prompt = "> "
		d.ti.SetValue(value)
		d.ti.Focus()
	}
	return d
}

func (d *editDialog) value() string {
	if d.multiline {
		return d.ta.Value()
	}
	return d.ti.Value()
}

func (d *editDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	switch k := msg.String(); {
	case k == "esc":
		return d.back, nil
	case k == "ctrl+s" || k == "enter" && !d.multiline:
		cmd, err := d.save(m, d.value())
		if err != nil {
			d.err = err.Error()
			return d, nil
		}
		return d.back, cmd
	}
	var cmd tea.Cmd
	if d.multiline {
		d.ta, cmd = d.ta.Update(msg)
	} else {
		d.ti, cmd = d.ti.Update(msg)
	}
	return d, cmd
}

func (d *editDialog) view(m *Model) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(d.title) + "\n")
	if d.hint != "" {
		b.WriteString(sMuted.Render(wrapLimit(d.hint, d.width-6, 3)) + "\n")
	}
	b.WriteString("\n")
	if d.multiline {
		b.WriteString(d.ta.View())
	} else {
		b.WriteString(d.ti.View())
	}
	if d.err != "" {
		b.WriteString("\n\n" + sBad.Render(wrapLimit(d.err, d.width-6, 3)))
	}
	return sDialog.Width(d.width).Render(b.String())
}

func (d *editDialog) help() string {
	if d.multiline {
		return helpLine("ctrl+s", "save", "enter", "new line", "esc", "cancel")
	}
	return helpLine("enter", "save", "esc", "cancel")
}

func (d *editDialog) resize(w, h int) {
	d.width = dialogWidth(w)
	if d.multiline { // single-line editors have no text area
		d.ta.SetWidth(d.width - 6)
	}
}

func (d *editDialog) passthrough(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if d.multiline {
		d.ta, cmd = d.ta.Update(msg)
	} else {
		d.ti, cmd = d.ti.Update(msg)
	}
	return cmd
}

// ---- default prompts ----

type promptsDialog struct {
	keys   []config.RuleKey
	cursor int
	width  int
}

func newPromptsDialog(w int) *promptsDialog {
	d := &promptsDialog{width: dialogWidth(w)}
	for _, k := range config.AllKinds {
		for _, t := range k.Triggers() {
			d.keys = append(d.keys, config.Key(k, t))
		}
	}
	return d
}

// builtIn is the template key would use without a custom prompt for it.
func builtIn(m *Model, key config.RuleKey) string {
	k, t, _ := key.Parse()
	c := m.cfg.Clone()
	delete(c.Prompts, key)
	return prompt.Template(c, k, t)
}

func (d *promptsDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	if moveCursor(msg, &d.cursor, len(d.keys), 8) {
		return d, nil
	}
	key := d.keys[d.cursor]
	switch msg.String() {
	case "esc", "q":
		return nil, nil
	case "r":
		if _, ok := m.cfg.Prompts[key]; !ok {
			return d, nil
		}
		delete(m.cfg.Prompts, key)
		if len(m.cfg.Prompts) == 0 {
			m.cfg.Prompts = nil
		}
		return d, m.saveConfig("%s uses the built-in prompt again", key)
	case "enter":
		k, t, _ := key.Parse()
		e := newEditDialog("Prompt · "+string(key),
			"Go template. Fields: .Repo .Number .Title .URL .Author .Body .Labels .Branch .HeadSHA .Checks .Severity .Trigger .Actor .Detail",
			prompt.Template(m.cfg, k, t), true, func(m *Model, v string) (tea.Cmd, error) {
				if err := config.ValidatePrompt(v); err != nil {
					return nil, err
				}
				if strings.TrimSpace(v) == strings.TrimSpace(builtIn(m, key)) {
					delete(m.cfg.Prompts, key)
					return m.saveConfig("%s matches the built-in prompt; nothing custom saved", key), nil
				}
				if m.cfg.Prompts == nil {
					m.cfg.Prompts = map[config.RuleKey]string{}
				}
				m.cfg.Prompts[key] = v
				return m.saveConfig("Saved the prompt for %s", key), nil
			}, m.width, m.height)
		e.back = d
		return e, textarea.Blink
	}
	return d, nil
}

func (d *promptsDialog) view(m *Model) string {
	var b strings.Builder
	b.WriteString(sTitle.Render("Prompts") + "\n" + sMuted.Render(wrapLimit(
		"Triggers without a prompt of their own use their kind's, with a line saying what triggered them.", d.width-6, 2)) + "\n\n")
	h := clamp(m.height-14, 5, len(d.keys))
	start := window(d.cursor, h, len(d.keys))
	for i := start; i < len(d.keys) && i < start+h; i++ {
		key := d.keys[i]
		k, t, _ := key.Parse()
		src := sMuted.Render("built-in")
		switch _, custom := m.cfg.Prompts[key]; {
		case custom:
			src = sWarn.Render("custom")
		case t != config.TriggerOpened && prompt.Defaults[key] == "":
			src = sMuted.Render("uses " + string(k) + "'s")
		}
		row := "  " + fit(string(key), 36) + " " + src
		if i == d.cursor {
			row = selectRow(row, d.width-6)
		}
		b.WriteString(row + "\n")
	}
	return sDialog.Width(d.width).Render(strings.TrimRight(b.String(), "\n"))
}

func (d *promptsDialog) help() string {
	return helpLine("enter", "edit", "r", "reset to built-in", "esc", "close")
}

func (d *promptsDialog) resize(w, h int)             { d.width = dialogWidth(w) }
func (d *promptsDialog) passthrough(tea.Msg) tea.Cmd { return nil }
