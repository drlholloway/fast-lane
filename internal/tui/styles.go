package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A48BFF"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#8A8A8A"}
	colGood   = lipgloss.AdaptiveColor{Light: "#1F7A3A", Dark: "#5FD38A"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6200", Dark: "#F2B64C"}
	colBad    = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#FF6B6B"}
	colSelBG  = lipgloss.AdaptiveColor{Light: "#E6E0FA", Dark: "#2E2650"}

	sTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	sMuted    = lipgloss.NewStyle().Foreground(colMuted)
	sGood     = lipgloss.NewStyle().Foreground(colGood)
	sWarn     = lipgloss.NewStyle().Foreground(colWarn)
	sBad      = lipgloss.NewStyle().Foreground(colBad)
	sBold     = lipgloss.NewStyle().Bold(true)
	sSelected = lipgloss.NewStyle().Background(colSelBG)
	sTabOn    = lipgloss.NewStyle().Bold(true).Foreground(colAccent).Underline(true)
	sTabOff   = lipgloss.NewStyle().Foreground(colMuted)
	sKey      = lipgloss.NewStyle().Foreground(colAccent)
	sDialog   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(1, 2)
	sAlert    = lipgloss.NewStyle().Bold(true).Foreground(colWarn)
)

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("Jan 06")
}

func kindStyle(k config.EventKind) lipgloss.Style {
	switch k {
	case config.KindDependabot, config.KindCodeScanning:
		return sWarn
	case config.KindPullRequest:
		return sGood
	}
	return lipgloss.NewStyle().Foreground(colAccent)
}

func severityStyle(sev string) lipgloss.Style {
	switch strings.ToLower(sev) {
	case "critical", "high", "error":
		return sBad
	case "medium", "moderate", "warning":
		return sWarn
	}
	return sMuted
}

func statusStyle(s claude.Status) lipgloss.Style {
	switch s {
	case claude.StatusRunning, claude.StatusPreparing:
		return sGood
	case claude.StatusWaiting:
		return sAlert
	case claude.StatusFailed:
		return sBad
	}
	return sMuted
}

func statusIcon(s claude.Status) string {
	switch s {
	case claude.StatusQueued:
		return "◌"
	case claude.StatusPreparing, claude.StatusRunning:
		return "●"
	case claude.StatusWaiting:
		return "!"
	case claude.StatusIdle:
		return "✓"
	case claude.StatusFailed:
		return "✗"
	}
	return "■"
}

// selectRow highlights a list row. Inner styles are stripped because their
// resets would end the highlight partway across the row.
func selectRow(row string, w int) string {
	return sSelected.Render(fit(ansi.Strip(row), w))
}

// window returns the first visible index of a scrolled list.
func window(cursor, height, total int) int {
	if height <= 0 || total <= height {
		return 0
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start
}

func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// helpLine renders "key desc · key desc".
func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+" "+sMuted.Render(pairs[i+1]))
	}
	return strings.Join(parts, sMuted.Render(" · "))
}
