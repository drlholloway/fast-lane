package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

type repoSource int

const (
	srcAll repoSource = iota
	srcWatched
	srcMine
	srcStarred
	srcMyPRs
)

var sourceNames = []string{"all", "watched", "mine", "starred", "with my PRs"}

type repoRow struct {
	github.Repo
	Owned   bool
	Starred bool
	MyPRs   int
}

// mergeRepos combines the user's repos, starred repos, and repos where the
// user has open pull requests into one list.
func mergeRepos(repos, starred []github.Repo, myPRs []github.Event, login string) []repoRow {
	byName := map[string]*repoRow{}
	var order []string
	add := func(r github.Repo) *repoRow {
		key := strings.ToLower(r.FullName)
		if row, ok := byName[key]; ok {
			return row
		}
		byName[key] = &repoRow{Repo: r}
		order = append(order, key)
		return byName[key]
	}
	for _, r := range repos {
		row := add(r)
		row.Owned = strings.HasPrefix(strings.ToLower(r.FullName), strings.ToLower(login)+"/")
	}
	for _, r := range starred {
		add(r).Starred = true
	}
	for _, ev := range myPRs {
		add(github.Repo{FullName: ev.Repo, HTMLURL: "https://github.com/" + ev.Repo}).MyPRs++
	}
	out := make([]repoRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byName[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PushedAt.After(out[j].PushedAt) })
	return out
}

// visibleRepos applies the source and text filters. Watched repos missing
// from the API listing (e.g. access removed) are still shown.
func (m *Model) visibleRepos() []repoRow {
	rows := m.repos
	for _, rc := range m.cfg.Repos {
		found := false
		for _, r := range m.repos {
			if strings.EqualFold(r.FullName, rc.FullName) {
				found = true
				break
			}
		}
		if !found {
			rows = append(rows, repoRow{Repo: github.Repo{FullName: rc.FullName, HTMLURL: "https://github.com/" + rc.FullName}})
		}
	}
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	var out []repoRow
	for _, r := range rows {
		watched := m.cfg.Repo(r.FullName) != nil
		switch m.repoSource {
		case srcWatched:
			if !watched {
				continue
			}
		case srcMine:
			if !r.Owned {
				continue
			}
		case srcStarred:
			if !r.Starred {
				continue
			}
		case srcMyPRs:
			if r.MyPRs == 0 {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(r.FullName+" "+r.Description), q) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (m *Model) updateFilter(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filter.SetValue("")
		fallthrough
	case "enter":
		m.filtering = false
		m.filter.Blur()
		m.repoCursor = 0
		return nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.repoCursor = 0
	return cmd
}

func (m *Model) reposKey(msg tea.KeyMsg) tea.Cmd {
	rows := m.visibleRepos()
	if moveCursor(msg, &m.repoCursor, len(rows), m.bodyHeight()-2) {
		return nil
	}
	switch msg.String() {
	case "/":
		m.filtering = true
		return m.filter.Focus()
	case "esc":
		m.filter.SetValue("")
		return nil
	case "f":
		m.repoSource = (m.repoSource + 1) % repoSource(len(sourceNames))
		m.repoCursor = 0
		return nil
	}
	if m.repoCursor >= len(rows) {
		return nil
	}
	r := rows[m.repoCursor]
	switch msg.String() {
	case " ", "enter":
		if m.cfg.Repo(r.FullName) != nil {
			m.cfg.Unwatch(r.FullName)
			delete(m.events, strings.ToLower(r.FullName))
			delete(m.pollErrs, r.FullName)
			m.rebuildFeed()
			return m.saveConfig("Stopped watching %s", r.FullName)
		}
		rc := m.cfg.Watch(r.FullName)
		cmd := m.saveConfig("Watching %s", r.FullName)
		return tea.Batch(cmd, m.pollRepo(*rc))
	case "a":
		rc := m.cfg.Repo(r.FullName)
		if rc == nil {
			m.setStatus(false, "Watch %s first (space)", r.FullName)
			return nil
		}
		rc.AutoRun = !rc.AutoRun
		if rc.AutoRun && m.cfg.RulesFor(*rc) != nil {
			return m.saveConfig("Auto-run on for %s: new events matching your auto rules start Claude sessions without asking", r.FullName)
		}
		if rc.AutoRun {
			return m.saveConfig("Auto-run on for %s: new events start Claude sessions without asking", r.FullName)
		}
		return m.saveConfig("Auto-run off for %s", r.FullName)
	case "e":
		rc := m.cfg.Repo(r.FullName)
		if rc == nil {
			m.setStatus(false, "Watch %s first (space)", r.FullName)
			return nil
		}
		m.dialog = newKindsDialog(*rc, m.width)
	case "r":
		if m.cfg.Repo(r.FullName) == nil {
			m.setStatus(false, "Watch %s first (space)", r.FullName)
			return nil
		}
		m.ruleScope, m.ruleCursor, m.tab = r.FullName, 0, tabRules
	case "o":
		return m.open(r.HTMLURL)
	}
	return nil
}

func (m *Model) saveConfig(format string, args ...any) tea.Cmd {
	if err := m.cfg.Save(); err != nil {
		m.setStatus(true, "Could not save config: %v", err)
		return nil
	}
	m.setStatus(false, format, args...)
	return nil
}

func (m *Model) reposView(h int) string {
	var b strings.Builder
	var src []string
	for i, n := range sourceNames {
		if repoSource(i) == m.repoSource {
			src = append(src, sTabOn.Render(n))
		} else {
			src = append(src, sTabOff.Render(n))
		}
	}
	top := "show: " + strings.Join(src, " ")
	if m.filtering || m.filter.Value() != "" {
		top += "   " + m.filter.View()
	}
	b.WriteString(top)
	b.WriteString("\n")
	h--

	switch {
	case m.loading:
		b.WriteString("\n  " + m.spinner.View() + " Loading repositories from GitHub…")
		return b.String()
	case m.loadErr != nil:
		b.WriteString("\n  " + sBad.Render("Could not load repositories: "+m.loadErr.Error()) + "\n\n  " +
			sMuted.Render("Check `gh auth status`, then press R to retry."))
		return b.String()
	}
	rows := m.visibleRepos()
	if len(rows) == 0 {
		b.WriteString("\n  " + sMuted.Render("No repositories match."))
		return b.String()
	}
	m.repoCursor = clamp(m.repoCursor, 0, len(rows)-1)
	wFlags := 14
	wName := clamp(m.width/3, 20, 45)
	wDesc := m.width - wName - wFlags - 4
	start := window(m.repoCursor, h, len(rows))
	for i := start; i < len(rows) && i < start+h; i++ {
		r := rows[i]
		check := "[ ]"
		var flags []string
		if rc := m.cfg.Repo(r.FullName); rc != nil {
			check = sGood.Render("[✓]")
			if rc.AutoRun {
				flags = append(flags, sAlert.Render("auto"))
			}
			if len(rc.Events) != len(config.AllKinds) {
				flags = append(flags, sMuted.Render(fmt.Sprintf("%d/%d", len(rc.Events), len(config.AllKinds))))
			}
		}
		if r.Starred {
			flags = append(flags, sWarn.Render("★"))
		}
		if r.MyPRs > 0 {
			flags = append(flags, sGood.Render(fmt.Sprintf("%dPR", r.MyPRs)))
		}
		if r.Private {
			flags = append(flags, sMuted.Render("priv"))
		}
		if r.Archived {
			flags = append(flags, sMuted.Render("arch"))
		}
		row := check + " " + fit(r.FullName, wName) + " " + fit(strings.Join(flags, " "), wFlags) + " " + sMuted.Render(fit(r.Description, wDesc))
		if i == m.repoCursor {
			row = selectRow(row, m.width)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}
