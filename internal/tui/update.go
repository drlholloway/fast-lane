package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/version"
)

// Fast Lane looks for a newer release of itself when it starts and once a
// day after that.

const (
	releaseRepo  = "drlholloway/fast-lane"
	releaseEvery = 24 * time.Hour
)

type (
	releaseMsg struct {
		rel github.Release
		err error
	}
	releaseTickMsg struct{}
)

func releaseTick() tea.Cmd {
	return tea.Tick(releaseEvery, func(time.Time) tea.Msg { return releaseTickMsg{} })
}

func (m *Model) checkRelease() tea.Cmd {
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rel, err := gh.LatestRelease(ctx, releaseRepo)
		return releaseMsg{rel: rel, err: err}
	}
}

func (m *Model) onRelease(msg releaseMsg) {
	m.releaseErr = msg.err
	if msg.err == nil {
		m.latest, m.checkedAt = msg.rel, time.Now()
	}
}

// updateAvailable reports whether a release newer than this build is out.
func (m *Model) updateAvailable() bool {
	return m.latest.Tag != "" && m.Outdated(m.latest.Tag, m.latest.Published)
}

// updateNotice is the footer line announcing a new release, or "".
func (m *Model) updateNotice() string {
	if !m.updateAvailable() {
		return ""
	}
	return fmt.Sprintf("⬆ Fast Lane %s is out (you have %s). Settings → Version opens its release page.",
		m.latest.Tag, version.String())
}

// versionValue is the Version setting: this build, and how it compares
// with the newest release.
func (m *Model) versionValue() string {
	v := version.String()
	switch {
	case m.updateAvailable():
		return v + "  " + sWarn.Render("⬆ "+m.latest.Tag+" is out")
	case m.checkedAt.IsZero() && m.releaseErr != nil:
		return v + "  " + sMuted.Render("couldn't check for a newer release")
	case m.checkedAt.IsZero():
		return v
	case m.latest.Tag == "":
		return v + "  " + sMuted.Render("no releases yet")
	}
	return v + "  " + sMuted.Render("up to date (newest is "+m.latest.Tag+")")
}

// openRelease opens the newest release's page when it's newer than this
// build, and the list of releases otherwise.
func (m *Model) openRelease() tea.Cmd {
	if m.updateAvailable() && m.latest.URL != "" {
		return m.open(m.latest.URL)
	}
	return m.open(repoURL + "/releases")
}
