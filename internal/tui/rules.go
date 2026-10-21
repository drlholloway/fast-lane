package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/prompt"
)

// The Rules tab edits auto_rules for one scope at a time: the global rules
// ("") or one watched repo's. Changes are saved to the config right away and
// apply from the next poll.

type ruleRow struct {
	key  config.RuleKey
	idx  int
	rule config.Rule
}

// ruleScopes lists global, then every watched repo.
func (m *Model) ruleScopes() []string {
	out := []string{""}
	for _, r := range m.cfg.Repos {
		out = append(out, r.FullName)
	}
	return out
}

// scope returns the current scope, falling back to global when its repo is
// no longer watched.
func (m *Model) scope() string {
	if m.ruleScope != "" && m.cfg.Repo(m.ruleScope) == nil {
		m.ruleScope = ""
	}
	return m.ruleScope
}

// ownRules returns the rules defined in scope itself (nil when a repo has
// none and inherits the global ones).
func (m *Model) ownRules(scope string) map[config.RuleKey]config.Rules {
	if scope == "" {
		return m.cfg.AutoRules
	}
	return m.cfg.Repo(scope).AutoRules
}

func (m *Model) setOwnRules(scope string, rules map[config.RuleKey]config.Rules) {
	for k, rs := range rules {
		if len(rs) == 0 {
			delete(rules, k)
		}
	}
	if len(rules) == 0 {
		rules = nil
	}
	if scope == "" {
		m.cfg.AutoRules = rules
	} else {
		m.cfg.Repo(scope).AutoRules = rules
	}
}

// inherited reports whether scope is a repo using the global rules.
func (m *Model) inherited(scope string) bool {
	return scope != "" && len(m.ownRules(scope)) == 0
}

// ruleRows lists the rules in effect for the current scope in kind and
// trigger order, each key's rules in the order they are tried.
func (m *Model) ruleRows() []ruleRow {
	rules := m.ownRules(m.scope())
	if m.inherited(m.scope()) {
		rules = m.cfg.AutoRules
	}
	var out []ruleRow
	for _, k := range config.AllKinds {
		for _, t := range k.Triggers() {
			key := config.Key(k, t)
			for i, r := range rules[key] {
				out = append(out, ruleRow{key, i, r})
			}
		}
	}
	return out
}

func copyRules(rules map[config.RuleKey]config.Rules) map[config.RuleKey]config.Rules {
	out := make(map[config.RuleKey]config.Rules, len(rules))
	for k, rs := range rules {
		out[k] = slices.Clone(rs)
	}
	return out
}

func (m *Model) rulesKey(msg tea.KeyMsg) tea.Cmd {
	rows := m.ruleRows()
	if moveCursor(msg, &m.ruleCursor, len(rows), m.bodyHeight()-4) {
		return nil
	}
	scope := m.scope()
	scopes := m.ruleScopes()
	i := slices.Index(scopes, scope)
	switch msg.String() {
	case "[", "left", "h":
		m.ruleScope, m.ruleCursor = scopes[(i+len(scopes)-1)%len(scopes)], 0
		return nil
	case "]", "right", "l":
		m.ruleScope, m.ruleCursor = scopes[(i+1)%len(scopes)], 0
		return nil
	case "i":
		return tea.Batch(m.cycleInbox(), m.runChecks())
	case "a":
		if scope == "" {
			m.setStatus(false, "Auto-run is set per repo; switch scope with [ or ]")
			return nil
		}
		rc := m.cfg.Repo(scope)
		rc.AutoRun = !rc.AutoRun
		if rc.AutoRun {
			return m.saveConfig("Auto-run on for %s", scope)
		}
		return m.saveConfig("Auto-run off for %s", scope)
	case "c":
		if scope == "" {
			return nil
		}
		if !m.inherited(scope) {
			m.setStatus(false, "%s already has its own rules", scope)
			return nil
		}
		m.setOwnRules(scope, copyRules(m.cfg.AutoRules))
		return m.saveConfig("Copied the global rules to %s; edit them here", scope)
	case "n":
		m.dialog = newRuleDialog(m, scope, "", -1, config.Rule{}, config.KindPullRequest, config.TriggerOpened)
		return nil
	}
	if m.ruleCursor >= len(rows) {
		return nil
	}
	row := rows[m.ruleCursor]
	switch msg.String() {
	case "enter", "e", "d", "J", "K":
		if m.inherited(scope) {
			m.setStatus(false, "%s uses the global rules: press c to copy them here, or edit them under Global", scope)
			return nil
		}
	}
	switch msg.String() {
	case "enter", "e":
		k, t, _ := row.key.Parse()
		m.dialog = newRuleDialog(m, scope, row.key, row.idx, row.rule, k, t)
	case "d":
		key, idx := row.key, row.idx
		m.dialog = newConfirmDialog(fmt.Sprintf("Delete rule %d for %s?", idx+1, key), func(m *Model) tea.Cmd {
			rules := copyRules(m.ownRules(scope))
			rules[key] = slices.Delete(rules[key], idx, idx+1)
			m.setOwnRules(scope, rules)
			m.ruleCursor = clamp(m.ruleCursor, 0, len(m.ruleRows())-1)
			return m.saveConfig("Deleted rule %d for %s", idx+1, key)
		}, m.width)
	case "K", "J":
		to := row.idx - 1
		if msg.String() == "J" {
			to = row.idx + 1
		}
		rules := copyRules(m.ownRules(scope))
		rs := rules[row.key]
		if to < 0 || to >= len(rs) {
			return nil
		}
		rs[row.idx], rs[to] = rs[to], rs[row.idx]
		m.setOwnRules(scope, rules)
		m.ruleCursor += to - row.idx
		return m.saveConfig("Moved rule for %s to position %d", row.key, to+1)
	}
	return nil
}

func (m *Model) rulesView(h int) string {
	var b strings.Builder
	scope := m.scope()
	name := "Global"
	if scope != "" {
		name = scope
	}
	scopes := m.ruleScopes()
	b.WriteString(sMuted.Render("scope ") + sTabOn.Render(name) +
		sMuted.Render(fmt.Sprintf("  %d/%d · [ ] to switch", slices.Index(scopes, scope)+1, len(scopes))) + "\n")

	in := inboxLabel(m.cfg.Inbox)
	if m.cfg.Inbox.Enabled && m.cfg.Inbox.AutoRun {
		in = sGood.Render(in)
	}
	line := sMuted.Render("inbox ") + in + sMuted.Render(" (i)")
	if scope != "" {
		ar := "off"
		if m.cfg.Repo(scope).AutoRun {
			ar = sGood.Render("on")
		}
		line += sMuted.Render("   auto-run ") + ar + sMuted.Render(" (a)")
	}
	b.WriteString(line + "\n")
	h -= 2

	switch {
	case m.inherited(scope):
		b.WriteString(sWarn.Render(fit("Uses the global rules (dimmed). c copies them here to customize; n adds rules that replace them.", m.width)) + "\n")
		h--
	case scope != "":
		b.WriteString(sMuted.Render(fit("This repo's rules replace the global ones. Delete them all to go back to the global rules.", m.width)) + "\n")
		h--
	default:
		var own []string
		for _, r := range m.cfg.Repos {
			if len(r.AutoRules) > 0 {
				own = append(own, r.FullName)
			}
		}
		if len(own) > 0 {
			b.WriteString(sMuted.Render(fit("Not used by "+strings.Join(own, ", ")+", which have their own rules.", m.width)) + "\n")
			h--
		}
	}

	rows := m.ruleRows()
	if len(rows) == 0 {
		b.WriteString("\n  " + sBold.Render("No auto rules.") + "\n\n  " + sMuted.Render(
			"Every newly opened item auto-runs on repos with auto-run on, and other triggers don't.\n  Press n to add a rule."))
		return b.String()
	}
	const (
		wKey    = 31
		wNum    = 2
		wPrompt = 7
	)
	wAllow := clamp(m.width/4, 12, 34)
	wFilter := m.width - wKey - wNum - wAllow - wPrompt - 6
	b.WriteString(sMuted.Render(fit("  "+fit("KEY", wKey)+" "+fit("#", wNum)+" "+fit("FILTERS", wFilter)+" "+
		fit("ALLOW", wAllow)+" PROMPT", m.width)) + "\n")
	h--
	m.ruleCursor = clamp(m.ruleCursor, 0, len(rows)-1)
	start := window(m.ruleCursor, h, len(rows))
	for i := start; i < len(rows) && i < start+h; i++ {
		r := rows[i]
		allow := "—"
		if len(r.rule.Allow) > 0 {
			names := make([]string, len(r.rule.Allow))
			for j, a := range r.rule.Allow {
				names[j] = string(a)
			}
			allow = strings.Join(names, " ")
		}
		pr := sMuted.Render("default")
		if r.rule.Prompt != "" {
			pr = sWarn.Render("custom")
		}
		row := "  " + fit(string(r.key), wKey) + " " + fit(fmt.Sprint(r.idx+1), wNum) + " " +
			fit(ruleSummary(r.rule), wFilter) + " " + fit(allow, wAllow) + " " + pr
		if m.inherited(scope) {
			row = sMuted.Render(ansi.Strip(row))
		}
		if i == m.ruleCursor {
			row = selectRow(row, m.width)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// ruleSummary describes a rule's filters in one line.
func ruleSummary(r config.Rule) string {
	var parts []string
	if r.MinSeverity != "" {
		parts = append(parts, "severity ≥ "+r.MinSeverity)
	}
	list := func(label string, v []string) {
		if len(v) > 0 {
			parts = append(parts, label+" "+strings.Join(v, ","))
		}
	}
	list("labels", r.Labels)
	list("not labels", r.SkipLabels)
	list("authors", r.Authors)
	list("not authors", r.SkipAuthors)
	if r.SkipDrafts {
		parts = append(parts, "no drafts")
	}
	if r.SkipBots {
		parts = append(parts, "no bots")
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, "; ")
}

// ---- rule form ----

type ruleField int

const (
	fKind ruleField = iota
	fTrigger
	fSeverity
	fLabels
	fSkipLabels
	fAuthors
	fSkipAuthors
	fSkipDrafts
	fSkipBots
	fAllow
	fPrompt
)

var severities = []string{"", "low", "medium", "high", "critical"}

type ruleDialog struct {
	scope   string
	origKey config.RuleKey
	origIdx int // -1 for a new rule

	kind       config.EventKind
	trigger    config.Trigger
	severity   string
	inputs     map[ruleField]*textinput.Model
	skipDrafts bool
	skipBots   bool
	allow      map[config.Action]bool
	allowAt    int
	prompt     string // empty means the default template

	cursor  int
	editing bool // the prompt editor is open
	ta      textarea.Model
	err     string
	width   int
	height  int
}

func newRuleDialog(m *Model, scope string, key config.RuleKey, idx int, r config.Rule, k config.EventKind, t config.Trigger) *ruleDialog {
	d := &ruleDialog{scope: scope, origKey: key, origIdx: idx, kind: k, trigger: t, severity: r.MinSeverity,
		skipDrafts: r.SkipDrafts, skipBots: r.SkipBots, prompt: r.Prompt, allow: map[config.Action]bool{},
		inputs: map[ruleField]*textinput.Model{}, width: dialogWidth(m.width), height: m.height}
	for _, a := range r.Allow {
		d.allow[a] = true
	}
	for f, v := range map[ruleField][]string{fLabels: r.Labels, fSkipLabels: r.SkipLabels, fAuthors: r.Authors, fSkipAuthors: r.SkipAuthors} {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = "comma-separated"
		ti.SetValue(strings.Join(v, ", "))
		d.inputs[f] = &ti
	}
	return d
}

func (d *ruleDialog) alert() bool {
	return d.kind == config.KindDependabot || d.kind == config.KindCodeScanning
}

// fields lists the rows that apply to the current kind.
func (d *ruleDialog) fields() []ruleField {
	fs := []ruleField{fKind, fTrigger}
	if d.alert() {
		fs = append(fs, fSeverity)
	} else {
		fs = append(fs, fLabels, fSkipLabels)
	}
	fs = append(fs, fAuthors, fSkipAuthors)
	if d.kind == config.KindPullRequest {
		fs = append(fs, fSkipDrafts)
	}
	return append(fs, fSkipBots, fAllow, fPrompt)
}

func (d *ruleDialog) field() ruleField {
	fs := d.fields()
	d.cursor = clamp(d.cursor, 0, len(fs)-1)
	return fs[d.cursor]
}

// focus moves keyboard focus to the text input under the cursor, if any.
func (d *ruleDialog) focus() tea.Cmd {
	var cmd tea.Cmd
	for f, ti := range d.inputs {
		if f == d.field() {
			cmd = ti.Focus()
		} else {
			ti.Blur()
		}
	}
	return cmd
}

func cycle[T comparable](opts []T, cur T, step int) T {
	i := slices.Index(opts, cur)
	if i < 0 {
		return opts[0]
	}
	return opts[(i+step+len(opts))%len(opts)]
}

func (d *ruleDialog) update(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	if d.editing {
		return d.updatePrompt(m, msg)
	}
	d.err = ""
	key := msg.String()
	switch key {
	case "esc":
		return nil, nil
	case "ctrl+s":
		return d.save(m)
	case "up", "shift+tab":
		d.cursor = clamp(d.cursor-1, 0, len(d.fields())-1)
		return d, d.focus()
	case "down", "tab":
		d.cursor = clamp(d.cursor+1, 0, len(d.fields())-1)
		return d, d.focus()
	}
	step := 0
	switch key {
	case "left":
		step = -1
	case "right", " ":
		step = 1
	}
	switch f := d.field(); f {
	case fKind:
		if step != 0 {
			d.kind = cycle(config.AllKinds, d.kind, step)
			if !slices.Contains(d.kind.Triggers(), d.trigger) {
				d.trigger = config.TriggerOpened
			}
		}
	case fTrigger:
		if step != 0 {
			d.trigger = cycle(d.kind.Triggers(), d.trigger, step)
		}
	case fSeverity:
		if step != 0 {
			d.severity = cycle(severities, d.severity, step)
		}
	case fSkipDrafts, fSkipBots:
		if key == " " || key == "enter" {
			if f == fSkipDrafts {
				d.skipDrafts = !d.skipDrafts
			} else {
				d.skipBots = !d.skipBots
			}
		}
	case fAllow:
		switch key {
		case "left":
			d.allowAt = clamp(d.allowAt-1, 0, len(config.AllActions)-1)
		case "right":
			d.allowAt = clamp(d.allowAt+1, 0, len(config.AllActions)-1)
		case " ", "enter":
			a := config.AllActions[d.allowAt]
			d.allow[a] = !d.allow[a]
		}
	case fPrompt:
		switch key {
		case "enter":
			text := d.prompt
			if text == "" {
				text = prompt.Template(m.cfg, d.kind, d.trigger)
			}
			d.ta = newTextarea(d.width-6, clamp(d.height-16, 5, 30), text)
			d.editing = true
			return d, textarea.Blink
		case "r":
			d.prompt = ""
		}
	default:
		ti := d.inputs[f]
		var cmd tea.Cmd
		*ti, cmd = ti.Update(msg)
		return d, cmd
	}
	return d, nil
}

func (d *ruleDialog) updatePrompt(m *Model, msg tea.KeyMsg) (dialog, tea.Cmd) {
	def := prompt.Template(m.cfg, d.kind, d.trigger)
	switch msg.String() {
	case "esc":
		d.editing = false
		return d, nil
	case "ctrl+s":
		d.prompt = d.ta.Value()
		if strings.TrimSpace(d.prompt) == strings.TrimSpace(def) {
			d.prompt = ""
		}
		d.editing = false
		return d, nil
	case "ctrl+r":
		d.ta.SetValue(def)
		return d, nil
	}
	var cmd tea.Cmd
	d.ta, cmd = d.ta.Update(msg)
	return d, cmd
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// rule builds the rule from the form, dropping fields the kind hides.
func (d *ruleDialog) rule() config.Rule {
	shown := d.fields()
	text := func(f ruleField) []string {
		if !slices.Contains(shown, f) {
			return nil
		}
		return splitList(d.inputs[f].Value())
	}
	r := config.Rule{
		Labels:      text(fLabels),
		SkipLabels:  text(fSkipLabels),
		Authors:     text(fAuthors),
		SkipAuthors: text(fSkipAuthors),
		SkipBots:    d.skipBots,
		Prompt:      d.prompt,
	}
	if slices.Contains(shown, fSeverity) {
		r.MinSeverity = d.severity
	}
	if slices.Contains(shown, fSkipDrafts) {
		r.SkipDrafts = d.skipDrafts
	}
	for _, a := range config.AllActions {
		if d.allow[a] {
			r.Allow = append(r.Allow, a)
		}
	}
	return r
}

func (d *ruleDialog) save(m *Model) (dialog, tea.Cmd) {
	r, key := d.rule(), config.Key(d.kind, d.trigger)
	if err := config.ValidateRule(key, r); err != nil {
		d.err = err.Error()
		return d, nil
	}
	if d.scope != "" && m.cfg.Repo(d.scope) == nil {
		d.err = d.scope + " is no longer watched"
		return d, nil
	}
	rules := copyRules(m.ownRules(d.scope))
	at := len(rules[key])
	if d.origIdx >= 0 {
		rules[d.origKey] = slices.Delete(rules[d.origKey], d.origIdx, d.origIdx+1)
		if d.origKey == key {
			at = d.origIdx
		} else {
			at = len(rules[key])
		}
	}
	rules[key] = slices.Insert(rules[key], at, r)
	m.setOwnRules(d.scope, rules)
	for i, row := range m.ruleRows() {
		if row.key == key && row.idx == at {
			m.ruleCursor = i
		}
	}
	verb := "Added"
	if d.origIdx >= 0 {
		verb = "Saved"
	}
	return nil, m.saveConfig("%s rule %d for %s", verb, at+1, key)
}

func (d *ruleDialog) view(m *Model) string {
	if d.editing {
		return d.promptView()
	}
	var b strings.Builder
	title := "New rule"
	if d.origIdx >= 0 {
		title = "Edit rule"
	}
	scope := "Global"
	if d.scope != "" {
		scope = d.scope
	}
	b.WriteString(sTitle.Render(title) + sMuted.Render(" · "+scope) + "\n\n")
	const wl = 14
	cur := d.field()
	for _, f := range d.fields() {
		var label, val string
		switch f {
		case fKind:
			label, val = "Kind", "‹ "+string(d.kind)+" ›"
		case fTrigger:
			label, val = "Trigger", "‹ "+string(d.trigger)+" ›"
		case fSeverity:
			sev := d.severity
			if sev == "" {
				sev = "any"
			}
			label, val = "Min severity", "‹ "+sev+" ›"
		case fLabels:
			label, val = "Labels", d.inputs[f].View()+sMuted.Render("  any of")
		case fSkipLabels:
			label, val = "Skip labels", d.inputs[f].View()
		case fAuthors:
			label, val = "Authors", d.inputs[f].View()+sMuted.Render("  any of")
		case fSkipAuthors:
			label, val = "Skip authors", d.inputs[f].View()
		case fSkipDrafts:
			label, val = "Skip drafts", checkbox(d.skipDrafts)
		case fSkipBots:
			label, val = "Skip bots", checkbox(d.skipBots)
		case fAllow:
			label, val = "Allow", d.allowView(f == cur, d.width-6-wl-2)
		case fPrompt:
			label = "Prompt"
			if d.prompt == "" {
				val = sMuted.Render("default")
			} else {
				first, _, _ := strings.Cut(strings.TrimSpace(d.prompt), "\n")
				val = sWarn.Render("custom: ") + first
			}
			if f == cur {
				val += sMuted.Render("  enter edit · r reset")
			}
		}
		marker := "  "
		if f == cur {
			marker = sKey.Render("› ")
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, marker+fit(label, wl), val)
		b.WriteString(row + "\n")
	}
	if d.err != "" {
		b.WriteString("\n" + sBad.Render(wrapLimit(d.err, d.width-6, 3)))
	}
	return sDialog.Width(d.width).Render(strings.TrimRight(b.String(), "\n"))
}

func checkbox(on bool) string {
	if on {
		return sGood.Render("[✓]")
	}
	return "[ ]"
}

func (d *ruleDialog) allowView(focused bool, w int) string {
	items := make([]string, len(config.AllActions))
	for i, a := range config.AllActions {
		item := checkbox(d.allow[a]) + " " + string(a)
		if focused && i == d.allowAt {
			item = sSelected.Render(ansi.Strip(item))
		}
		items[i] = item
	}
	// Wrap by hand so items never split across lines.
	var lines []string
	line := ""
	for _, it := range items {
		if line != "" && lipgloss.Width(line)+2+lipgloss.Width(it) > w {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += "  "
		}
		line += it
	}
	return strings.Join(append(lines, line), "\n")
}

func (d *ruleDialog) promptView() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("Prompt") + sMuted.Render(" · "+string(config.Key(d.kind, d.trigger))) + "\n")
	b.WriteString(sMuted.Render(wrapLimit("Go template. Fields: .Repo .Number .Title .URL .Author .Body .Labels "+
		".Branch .HeadSHA .Checks .Severity .Trigger .Actor .Detail", d.width-6, 3)) + "\n\n")
	b.WriteString(d.ta.View())
	return sDialog.Width(d.width).Render(b.String())
}

func (d *ruleDialog) help() string {
	if d.editing {
		return helpLine("ctrl+s", "keep", "ctrl+r", "reset to default", "alt+enter", "newline", "esc", "discard")
	}
	switch d.field() {
	case fKind, fTrigger, fSeverity:
		return helpLine("←/→", "change", "↑/↓", "move", "ctrl+s", "save", "esc", "cancel")
	case fAllow:
		return helpLine("←/→", "pick", "space", "toggle", "↑/↓", "move", "ctrl+s", "save", "esc", "cancel")
	case fSkipDrafts, fSkipBots:
		return helpLine("space", "toggle", "↑/↓", "move", "ctrl+s", "save", "esc", "cancel")
	}
	return helpLine("↑/↓", "move", "ctrl+s", "save", "esc", "cancel")
}

func (d *ruleDialog) resize(w, h int) {
	d.width, d.height = dialogWidth(w), h
	d.ta.SetWidth(d.width - 6)
}

func (d *ruleDialog) passthrough(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if d.editing {
		d.ta, cmd = d.ta.Update(msg)
		return cmd
	}
	if ti, ok := d.inputs[d.field()]; ok {
		*ti, cmd = ti.Update(msg)
	}
	return cmd
}
