// Command fast-lane is a TUI that watches GitHub repositories and hands
// routine maintenance to Claude Code sessions.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/state"
	"github.com/drlholloway/fast-lane/internal/tui"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fast-lane:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultPath(), "path to the config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("fast-lane", version)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required: https://cli.github.com")
	}
	// A missing claude is reported on the Settings tab, where its path can be
	// fixed, rather than stopping Fast Lane from starting.
	gh, err := github.NewREST()
	if err != nil {
		return err
	}

	stateDir := config.StateHome()
	st, err := state.Load(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return err
	}
	mgr := claude.NewManager(cfg, filepath.Join(stateDir, "logs"))
	mgr.Restore(st.Sessions)
	defer mgr.Shutdown()

	_, err = tea.NewProgram(tui.New(cfg, st, gh, mgr), tea.WithAltScreen()).Run()
	return err
}
