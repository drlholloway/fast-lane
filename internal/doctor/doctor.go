// Package doctor checks that everything Fast Lane relies on is installed,
// signed in and writable, and says how to fix what is not.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/local"
)

// Status is the outcome of one check.
type Status int

const (
	OK Status = iota
	// Warn means Fast Lane works, but something is limited.
	Warn
	// Fail means something Fast Lane needs is missing.
	Fail
)

// Check is one prerequisite and what was found.
type Check struct {
	Name   string
	Status Status
	Detail string
	// Fix says what to do when Status is not OK.
	Fix string
}

// Env is what the checks look at. Tests swap its functions out.
type Env struct {
	Config *config.Config
	GitHub github.Client
	// LookPath and Output default to exec.LookPath and running the command.
	LookPath func(string) (string, error)
	Output   func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (e *Env) defaults() {
	if e.LookPath == nil {
		e.LookPath = exec.LookPath
	}
	if e.Output == nil {
		e.Output = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
}

// Run performs every check in order.
func Run(ctx context.Context, e Env) []Check {
	e.defaults()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := []Check{e.tool(ctx, "GitHub CLI", "gh", "Install it from https://cli.github.com.")}
	auth, login := e.github(ctx)
	out = append(out, auth)
	if e.Config.Inbox.Enabled {
		out = append(out, e.notifications(ctx, login))
	}
	out = append(out,
		e.tool(ctx, "Claude Code", e.Config.Claude.Path,
			"Install it from https://claude.com/claude-code and run claude once to sign in, or set Claude path below."),
		e.git(ctx),
		e.workspace(),
		e.codeDirs(),
		e.configFile(),
	)
	return out
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

func (e *Env) tool(ctx context.Context, name, bin, fix string) Check {
	c := Check{Name: name}
	path, err := e.LookPath(bin)
	if err != nil {
		c.Status, c.Detail, c.Fix = Fail, bin+" is not on your PATH", fix
		return c
	}
	v, err := e.Output(ctx, path, "--version")
	if err != nil {
		c.Status, c.Detail, c.Fix = Fail, fmt.Sprintf("%s --version failed: %v", path, err), fix
		return c
	}
	c.Detail = firstLine(v)
	return c
}

func (e *Env) github(ctx context.Context) (Check, string) {
	c := Check{Name: "GitHub login"}
	a, err := e.GitHub.Auth(ctx)
	if err != nil {
		c.Status, c.Detail, c.Fix = Fail, err.Error(), "Run gh auth login."
		return c, ""
	}
	c.Detail = "@" + a.Login
	if !a.ScopesKnown {
		c.Detail += " (token without listed scopes; make sure it can read repositories, issues, pull requests and notifications)"
		return c, a.Login
	}
	c.Detail += " · scopes: " + strings.Join(a.Scopes, ", ")
	var missing []string
	if !slices.Contains(a.Scopes, "repo") {
		missing = append(missing, "repo")
	}
	if !slices.Contains(a.Scopes, "read:org") && !slices.Contains(a.Scopes, "admin:org") {
		missing = append(missing, "read:org")
	}
	if slices.Contains(a.Scopes, "delete_repo") && e.fullAuto() {
		c.Status = Warn
		c.Fix = "Your login can delete repositories. Full auto refuses to, but without this scope GitHub refuses too: " +
			"run gh auth refresh --remove-scopes delete_repo."
		return c, a.Login
	}
	if len(missing) > 0 {
		c.Status = Warn
		c.Fix = "Without repo, private repos, security alerts and notifications are out of reach; without read:org, " +
			"organization repos may be missing. Run gh auth refresh -s " + strings.Join(missing, ",") + "."
	}
	return c, a.Login
}

// fullAuto reports whether anything runs in full auto.
func (e *Env) fullAuto() bool {
	return e.Config.Inbox.FullAuto || slices.ContainsFunc(e.Config.Repos, func(r config.Repo) bool { return r.FullAuto })
}

func (e *Env) notifications(ctx context.Context, login string) Check {
	c := Check{Name: "Notifications"}
	if login == "" {
		c.Status, c.Detail, c.Fix = Fail, "needs a GitHub login", "Fix the GitHub login first."
		return c
	}
	if _, err := e.GitHub.Inbox(ctx, login, time.Now()); err != nil {
		c.Status, c.Detail = Warn, err.Error()
		c.Fix = "The inbox (review requests, mentions, assignments) will not work. Run gh auth refresh -s notifications, or turn the inbox off below."
		return c
	}
	c.Detail = "readable, for review requests, mentions and assignments"
	return c
}

var gitVersionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

func (e *Env) git(ctx context.Context) Check {
	c := e.tool(ctx, "git", "git", "Install git; Fast Lane clones repos and makes worktrees with it.")
	if c.Status != OK {
		return c
	}
	if m := gitVersionRE.FindStringSubmatch(c.Detail); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major < 2 || major == 2 && minor < 17 {
			c.Status, c.Fix = Fail, "Upgrade git to 2.17 or later; older versions cannot remove worktrees."
		}
	}
	return c
}

// writable reports whether files can be created in dir, creating it if needed.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".fast-lane-check-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

func (e *Env) workspace() Check {
	c := Check{Name: "Workspace", Detail: e.Config.WorkspaceDir}
	if err := writable(e.Config.WorkspaceDir); err != nil {
		c.Status, c.Detail, c.Fix = Fail, err.Error(), "Choose a workspace folder you can write to below."
	}
	return c
}

func (e *Env) codeDirs() Check {
	c := Check{Name: "Code folders"}
	var found []string
	seen := map[string]bool{}
	for i, d := range e.Config.SearchDirs() {
		real, err := filepath.EvalSymlinks(d)
		if err != nil || seen[strings.ToLower(real)] {
			continue
		}
		seen[strings.ToLower(real)] = true
		n := len(local.Scan([]string{d}))
		noun := "checkouts"
		if n == 1 {
			noun = "checkout"
		}
		found = append(found, fmt.Sprintf("%s (%d %s)", e.Config.CodeDirs[i], n, noun))
	}
	if len(found) == 0 {
		c.Status, c.Detail = Warn, "none of the code folders exist"
		c.Fix = "Repos will be cloned into the workspace. Add the folders where you keep code below to work from your checkouts."
		return c
	}
	c.Detail = strings.Join(found, ", ")
	return c
}

func (e *Env) configFile() Check {
	c := Check{Name: "Config file", Detail: e.Config.Path()}
	if err := writable(filepath.Dir(e.Config.Path())); err != nil {
		c.Status, c.Detail, c.Fix = Fail, err.Error(), "Settings cannot be saved. Fix the folder's permissions or start with -config <path>."
	}
	return c
}

// Worst returns the most serious status among checks.
func Worst(checks []Check) Status {
	w := OK
	for _, c := range checks {
		w = max(w, c.Status)
	}
	return w
}
