package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/local"
	"github.com/drlholloway/fast-lane/internal/prompt"
)

// Status is the lifecycle state of a session.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusPreparing Status = "preparing"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "needs you"
	StatusIdle      Status = "done"
	StatusFailed    Status = "failed"
	StatusStopped   Status = "stopped"
)

// Active reports whether the session holds a process slot.
func (s Status) Active() bool {
	return s == StatusPreparing || s == StatusRunning || s == StatusWaiting
}

const maxLines = 2000

// Session is one Claude Code conversation about one GitHub event.
type Session struct {
	ID       int          `json:"id"`
	Event    github.Event `json:"event"`
	Status   Status       `json:"status"`
	ClaudeID string       `json:"claude_session_id,omitempty"`
	CostUSD  float64      `json:"cost_usd"`
	Started  time.Time    `json:"started"`
	Updated  time.Time    `json:"updated"`
	Result   string       `json:"result,omitempty"`
	Err      string       `json:"error,omitempty"`
	Lines    []Line       `json:"lines,omitempty"`
	// Allow lists the actions Claude may take without asking.
	Allow []config.Action `json:"allow,omitempty"`
	// Model overrides the configured model when set.
	Model string `json:"model,omitempty"`
	// Dir is where Claude runs, set when the session first starts. When
	// Checkout is set, Dir is a git worktree of that local checkout, made
	// from its Remote.
	Dir      string `json:"dir,omitempty"`
	Checkout string `json:"checkout,omitempty"`
	Remote   string `json:"remote,omitempty"`
	// Pending holds requests waiting for the user, oldest first.
	Pending []Request `json:"-"`

	nextInput string
}

// Update tells the TUI that a session changed.
type Update struct {
	SessionID int
	// NeedsUser is set when the session just started waiting on the user.
	NeedsUser bool
}

type proc struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	stdin  io.WriteCloser
	closed bool
	killed bool
}

func (p *proc) write(line []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("session input is closed")
	}
	_, err := p.stdin.Write(append(line, '\n'))
	return err
}

func (p *proc) closeInput() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.stdin.Close()
	}
}

// Runner abstracts process creation so tests can stub out claude and gh.
type Runner func(ctx context.Context, dir, name string, args ...string) *exec.Cmd

func defaultRunner(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd
}

// Manager owns all sessions and schedules them within the concurrency limit.
type Manager struct {
	mu sync.Mutex
	// cfg is the manager's own copy, replaced whole by SetConfig and never
	// changed in place, so a session can keep using the one it started with.
	cfg      *config.Config
	sessions []*Session
	procs    map[int]*proc
	queue    []int
	nextID   int
	updates  chan Update
	logDir   string
	run      Runner
	ctx      context.Context
	cancel   context.CancelFunc
	// checkouts maps lower-case "owner/name" to a local checkout.
	checkouts map[string]local.Checkout
	// running tracks execute goroutines so Shutdown can wait for them.
	running sync.WaitGroup
}

// NewManager creates a manager that writes raw session logs to logDir.
func NewManager(cfg *config.Config, logDir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:     cfg.Clone(),
		procs:   map[int]*proc{},
		nextID:  1,
		updates: make(chan Update, 256),
		logDir:  logDir,
		run:     defaultRunner,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// SetRunner replaces the process factory. Intended for tests.
func (m *Manager) SetRunner(r Runner) { m.run = r }

// Updates delivers change notifications.
func (m *Manager) Updates() <-chan Update { return m.updates }

func (m *Manager) notify(u Update) {
	select {
	case m.updates <- u:
	default:
	}
}

// Restore loads sessions from a previous run. Anything that was in flight is
// marked stopped; finished sessions can still be replied to.
func (m *Manager) Restore(sessions []*Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range sessions {
		if s.Status.Active() || s.Status == StatusQueued {
			s.Status = StatusStopped
			s.Err = "interrupted when Fast Lane exited"
		}
		if s.ID >= m.nextID {
			m.nextID = s.ID + 1
		}
		m.sessions = append(m.sessions, s)
	}
}

// Snapshot returns copies of all sessions, newest first.
func (m *Manager) Snapshot() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for i := len(m.sessions) - 1; i >= 0; i-- {
		s := *m.sessions[i]
		s.Lines = s.Lines[:len(s.Lines):len(s.Lines)]
		s.Pending = append([]Request(nil), s.Pending...)
		out = append(out, s)
	}
	return out
}

// Persistable returns sessions in a form suitable for saving.
func (m *Manager) Persistable() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		c := *s
		if len(c.Lines) > 300 {
			c.Lines = c.Lines[len(c.Lines)-300:]
		}
		c.Pending = nil
		out = append(out, &c)
	}
	return out
}

// ActiveCount returns how many sessions are running or queued.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.procs) + len(m.queue)
}

// Start queues a new session for ev using its rendered prompt template.
func (m *Manager) Start(ev github.Event) (int, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	text, err := prompt.Render(cfg, ev, github.Opened(ev), "")
	if err != nil {
		return 0, err
	}
	return m.StartPrompt(ev, text, Options{}), nil
}

// Options shape a session: what Claude may do without asking and which
// model it runs.
type Options struct {
	Allow []config.Action
	// Model overrides the configured model when set.
	Model string
}

// StartPrompt queues a new session for ev with custom instructions and
// returns its ID.
func (m *Manager) StartPrompt(ev github.Event, text string, o Options) int {
	m.mu.Lock()
	now := time.Now()
	s := &Session{ID: m.nextID, Event: ev, Status: StatusQueued, Started: now, Updated: now,
		Allow: o.Allow, Model: o.Model, nextInput: text}
	m.nextID++
	m.sessions = append(m.sessions, s)
	m.queue = append(m.queue, s.ID)
	m.mu.Unlock()
	m.notify(Update{SessionID: s.ID})
	m.pump()
	return s.ID
}

// Reply sends text to a finished session, resuming the conversation.
func (m *Manager) Reply(id int, text string) error {
	return m.resume(id, text, nil)
}

// Resume is Reply for a trigger: it also replaces the session's options
// with the trigger rule's.
func (m *Manager) Resume(id int, text string, o Options) error {
	return m.resume(id, text, &o)
}

func (m *Manager) resume(id int, text string, o *Options) error {
	m.mu.Lock()
	s := m.find(id)
	switch {
	case s == nil:
		m.mu.Unlock()
		return fmt.Errorf("no session %d", id)
	case s.Status.Active() || s.Status == StatusQueued:
		m.mu.Unlock()
		return errors.New("session is still working; wait for it to finish its turn")
	case s.ClaudeID == "":
		m.mu.Unlock()
		return errors.New("session never started, so there is nothing to resume")
	}
	s.nextInput = text
	if o != nil {
		s.Allow, s.Model = o.Allow, o.Model
	}
	s.Status = StatusQueued
	s.Err = ""
	s.Updated = time.Now()
	s.Lines = appendLine(s.Lines, Line{LineUser, text})
	m.queue = append(m.queue, id)
	m.mu.Unlock()
	m.notify(Update{SessionID: id})
	m.pump()
	return nil
}

// Respond answers a pending request.
func (m *Manager) Respond(req Request, a Answer) error {
	line, err := controlResponse(req, a)
	if err != nil {
		return err
	}
	m.mu.Lock()
	s := m.find(req.SessionID)
	p := m.procs[req.SessionID]
	if s == nil || p == nil {
		m.mu.Unlock()
		return errors.New("session is no longer running")
	}
	for i, r := range s.Pending {
		if r.RequestID == req.RequestID {
			s.Pending = append(s.Pending[:i], s.Pending[i+1:]...)
			break
		}
	}
	verdict := "allowed"
	switch {
	case req.IsQuestion() && a.Allow:
		var parts []string
		for q, ans := range a.Answers {
			parts = append(parts, fmt.Sprintf("%s → %s", q, ans))
		}
		verdict = "answered: " + strings.Join(parts, "; ")
	case !a.Allow:
		verdict = "denied"
	case a.Always:
		verdict = "allowed for session"
	}
	s.Lines = appendLine(s.Lines, Line{LineUser, fmt.Sprintf("%s %s", req.ToolName, verdict)})
	if len(s.Pending) == 0 && s.Status == StatusWaiting {
		s.Status = StatusRunning
	}
	s.Updated = time.Now()
	m.mu.Unlock()
	m.notify(Update{SessionID: req.SessionID})
	return p.write(line)
}

// Stop kills a running session or removes it from the queue.
func (m *Manager) Stop(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, q := range m.queue {
		if q == id {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			if s := m.find(id); s != nil {
				s.Status = StatusStopped
			}
			go m.notify(Update{SessionID: id})
			return
		}
	}
	if p := m.procs[id]; p != nil {
		p.mu.Lock()
		p.killed = true
		cmd := p.cmd
		p.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

// Shutdown kills every running session and waits for it to exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.queue = nil
	procs := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	for _, p := range procs {
		p.mu.Lock()
		p.killed = true
		p.mu.Unlock()
	}
	m.cancel()
	m.running.Wait()
}

func (m *Manager) find(id int) *Session {
	for _, s := range m.sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// dirBusy reports whether a running session works in dir. Sessions that
// share a clone run one at a time; worktrees are per session.
func (m *Manager) dirBusy(dir string) bool {
	for id := range m.procs {
		if s := m.find(id); s != nil && s.Dir == dir {
			return true
		}
	}
	return false
}

// SetConfig makes later work use a copy of cfg. Turns already running keep
// the settings they started with.
func (m *Manager) SetConfig(cfg *config.Config) {
	c := cfg.Clone()
	m.mu.Lock()
	m.cfg = c
	m.mu.Unlock()
}

// SetCheckouts tells the manager where repositories are checked out on this
// machine, keyed by lower-case "owner/name".
func (m *Manager) SetCheckouts(c map[string]local.Checkout) {
	m.mu.Lock()
	m.checkouts = c
	m.mu.Unlock()
}

// CheckoutFor returns the local checkout sessions on repo make worktrees
// from: the repo's local_path when set, otherwise one found in code_dirs.
func (m *Manager) CheckoutFor(repo string) (local.Checkout, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkoutFor(repo)
}

func (m *Manager) checkoutFor(repo string) (local.Checkout, bool) {
	if r := m.cfg.Repo(repo); r != nil && r.LocalPath != "" {
		return local.Open(r.LocalPath, repo), true
	}
	c, ok := m.checkouts[strings.ToLower(repo)]
	return c, ok
}

// assignDir picks where a session runs the first time it starts.
func (m *Manager) assignDir(s *Session) {
	repo := s.Event.Repo
	co, ok := m.checkoutFor(repo)
	switch {
	case s.ClaudeID != "":
		// From before sessions had a Dir. Claude finds a conversation to
		// resume by its working directory, so stay where it started.
		if r := m.cfg.Repo(repo); r != nil && r.LocalPath != "" {
			s.Dir = r.LocalPath
		} else {
			s.Dir = m.cfg.CloneDir(repo)
		}
	case ok:
		s.Dir, s.Checkout, s.Remote = m.cfg.WorktreeDir(repo, s.ID), co.Dir, co.Remote
	default:
		s.Dir = m.cfg.CloneDir(repo)
	}
}

// pump starts queued sessions while slots are free. Sessions for the same
// repository run one at a time because they share a working copy.
func (m *Manager) pump() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < len(m.queue) && len(m.procs) < m.cfg.MaxConcurrent; {
		s := m.find(m.queue[i])
		if s == nil {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			continue
		}
		if s.Dir == "" {
			m.assignDir(s)
		}
		if m.dirBusy(s.Dir) {
			i++
			continue
		}
		m.queue = append(m.queue[:i], m.queue[i+1:]...)
		input := s.nextInput
		s.nextInput = ""
		s.Status = StatusPreparing
		s.Updated = time.Now()
		p := &proc{}
		m.procs[s.ID] = p
		m.running.Add(1)
		go m.execute(s.ID, p, input)
	}
}

func (m *Manager) setStatus(id int, st Status, line *Line) {
	m.mu.Lock()
	if s := m.find(id); s != nil {
		s.Status = st
		s.Updated = time.Now()
		if line != nil {
			s.Lines = appendLine(s.Lines, *line)
		}
	}
	m.mu.Unlock()
	m.notify(Update{SessionID: id})
}

func (m *Manager) execute(id int, p *proc, input string) {
	defer m.running.Done()
	defer func() {
		m.mu.Lock()
		delete(m.procs, id)
		m.mu.Unlock()
		m.notify(Update{SessionID: id})
		m.pump()
	}()

	m.mu.Lock()
	s := m.find(id)
	repo, number, resume, allow, model := s.Event.Repo, s.Event.Number, s.ClaudeID, s.Allow, s.Model
	dir, checkout, remote := s.Dir, s.Checkout, s.Remote
	cfg := m.cfg
	m.mu.Unlock()

	var err error
	switch {
	case checkout != "":
		// Also after a resume, in case the worktree was cleaned up.
		err = m.prepareWorktree(id, checkout, remote, dir)
	case resume == "":
		err = m.prepareWorkdir(id, repo, dir)
	}
	if err != nil {
		m.fail(id, fmt.Sprintf("preparing %s: %v", dir, err))
		return
	}

	c := cfg.Claude
	if model != "" {
		c.Model = model
	}
	err = m.runClaude(id, p, c, dir, repo, number, resume, allow, checkout != "", input)
	m.mu.Lock()
	defer m.mu.Unlock()
	s = m.find(id)
	p.mu.Lock()
	killed := p.killed
	p.mu.Unlock()
	switch {
	case killed:
		s.Status = StatusStopped
		s.Lines = appendLine(s.Lines, Line{LineInfo, "stopped"})
	case err != nil:
		s.Status = StatusFailed
		s.Err = err.Error()
		s.Lines = appendLine(s.Lines, Line{LineError, err.Error()})
	case s.Status != StatusFailed:
		s.Status = StatusIdle
	}
	s.Pending = nil
	s.Updated = time.Now()
}

func (m *Manager) fail(id int, msg string) {
	m.mu.Lock()
	if s := m.find(id); s != nil {
		s.Status = StatusFailed
		s.Err = msg
		s.Lines = appendLine(s.Lines, Line{LineError, msg})
	}
	m.mu.Unlock()
	m.notify(Update{SessionID: id})
}

// prepareWorktree creates a session's worktree from the user's checkout the
// first time, or again after it was cleaned up. Only fetch and worktree add
// run in the checkout, so its working tree, branch and changes are untouched.
func (m *Manager) prepareWorktree(id int, checkout, remote, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	m.setStatus(id, StatusPreparing, &Line{LineInfo, "fetching " + remote + " in " + checkout})
	if out, err := m.run(m.ctx, checkout, "git", "fetch", remote, "--prune", "--quiet").CombinedOutput(); err != nil {
		// Not fatal; the worktree starts from what is already fetched.
		m.setStatus(id, StatusPreparing, &Line{LineInfo, "git fetch failed: " + oneLine(string(out), 200)})
	}
	base := m.baseRef(checkout, remote)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	m.setStatus(id, StatusPreparing, &Line{LineInfo, "creating worktree " + dir + " at " + base})
	out, err := m.run(m.ctx, checkout, "git", "worktree", "add", "--detach", dir, base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, oneLine(string(out), 300))
	}
	return nil
}

// baseRef is the remote's default branch, falling back to the checkout's HEAD.
func (m *Manager) baseRef(checkout, remote string) string {
	for _, ref := range []string{remote + "/HEAD", remote + "/main", remote + "/master"} {
		if m.run(m.ctx, checkout, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil {
			return ref
		}
	}
	return "HEAD"
}

// removeWorktree deletes a session's worktree. Branches and commits made in
// it stay in the checkout's repository; uncommitted changes are lost.
func (m *Manager) removeWorktree(s Session) error {
	if s.Checkout == "" {
		return nil
	}
	if _, err := os.Stat(s.Dir); err != nil {
		return nil
	}
	out, err := m.run(context.Background(), s.Checkout, "git", "worktree", "remove", "--force", s.Dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %v: %s", err, oneLine(string(out), 300))
	}
	return nil
}

// Delete removes a finished session and its worktree.
func (m *Manager) Delete(id int) error {
	m.mu.Lock()
	i := slices.IndexFunc(m.sessions, func(s *Session) bool { return s.ID == id })
	switch {
	case i < 0:
		m.mu.Unlock()
		return fmt.Errorf("no session %d", id)
	case m.sessions[i].Status.Active() || m.sessions[i].Status == StatusQueued:
		m.mu.Unlock()
		return errors.New("session is still working; stop it first")
	}
	s := *m.sessions[i]
	m.sessions = slices.Delete(m.sessions, i, i+1)
	m.mu.Unlock()
	m.notify(Update{SessionID: id})
	return m.removeWorktree(s)
}

// PruneWorktrees removes the worktrees of sessions that finished more than
// maxAge ago and reports how many it removed. The sessions stay; resuming
// one creates its worktree again.
func (m *Manager) PruneWorktrees(maxAge time.Duration) (int, error) {
	m.mu.Lock()
	var old []Session
	for _, s := range m.sessions {
		if s.Checkout != "" && !s.Status.Active() && s.Status != StatusQueued && time.Since(s.Updated) > maxAge {
			old = append(old, *s)
		}
	}
	m.mu.Unlock()
	n := 0
	var errs []error
	for _, s := range old {
		if _, err := os.Stat(s.Dir); err != nil {
			continue
		}
		if err := m.removeWorktree(s); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// prepareWorkdir clones the repo on first use and fetches otherwise.
func (m *Manager) prepareWorkdir(id int, repo, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		m.setStatus(id, StatusPreparing, &Line{LineInfo, "fetching " + repo + " in " + dir})
		out, err := m.run(m.ctx, dir, "git", "fetch", "--all", "--prune", "--quiet").CombinedOutput()
		if err != nil {
			// A failed fetch is not fatal; Claude can still work on what is there.
			m.setStatus(id, StatusPreparing, &Line{LineInfo, "git fetch failed: " + oneLine(string(out), 200)})
		}
		return nil
	} else if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists but is not a git checkout", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	m.setStatus(id, StatusPreparing, &Line{LineInfo, "cloning " + repo + " into " + dir})
	out, err := m.run(m.ctx, filepath.Dir(dir), "gh", "repo", "clone", repo, dir, "--", "--quiet").CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh repo clone: %v: %s", err, oneLine(string(out), 300))
	}
	return nil
}

func (m *Manager) claudeArgs(c config.Claude, repo string, number int, resume string, allow []config.Action, worktree bool) ([]string, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
		"--append-system-prompt", prompt.System(allow, worktree),
	}
	if c.PermissionMode != "" {
		args = append(args, "--permission-mode", c.PermissionMode)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", c.MaxBudgetUSD))
	}
	tools := slices.Clone(c.AllowedTools)
	for _, a := range allow {
		for _, t := range a.Tools(repo, number) {
			if !slices.Contains(tools, t) {
				tools = append(tools, t)
			}
		}
	}
	if len(tools) > 0 {
		// Passing rules through --settings avoids splitting rules that
		// contain spaces or commas.
		settings, err := json.Marshal(map[string]any{"permissions": map[string]any{"allow": tools}})
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", string(settings))
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	return append(args, c.ExtraArgs...), nil
}

func (m *Manager) runClaude(id int, p *proc, c config.Claude, dir, repo string, number int, resume string, allow []config.Action, worktree bool, input string) error {
	args, err := m.claudeArgs(c, repo, number, resume, allow, worktree)
	if err != nil {
		return err
	}
	cmd := m.run(m.ctx, dir, c.Path, args...)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.killed {
		p.mu.Unlock()
		return errors.New("stopped")
	}
	p.cmd, p.stdin = cmd, stdin
	err = cmd.Start()
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start %s: %w", c.Path, err)
	}
	m.setStatus(id, StatusRunning, nil)

	var logf *os.File
	if m.logDir != "" {
		if err := os.MkdirAll(m.logDir, 0o755); err == nil {
			logf, _ = os.OpenFile(filepath.Join(m.logDir, fmt.Sprintf("session-%d.jsonl", id)),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		}
	}
	if logf != nil {
		defer logf.Close()
	}

	if err := p.write(userMessage(input)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	gotResult := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		if logf != nil {
			logf.Write(raw)
			logf.Write([]byte{'\n'})
		}
		var msg streamMsg
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if m.handle(id, p, msg) {
			gotResult = true
			p.closeInput()
		}
	}
	p.closeInput()
	waitErr := cmd.Wait()
	if gotResult {
		return nil
	}
	if waitErr == nil {
		waitErr = errors.New("claude exited without a result")
	}
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%v: %s", waitErr, oneLine(tail, 400))
	}
	return waitErr
}

// handle applies one stream message and reports whether it ended the turn.
func (m *Manager) handle(id int, p *proc, msg streamMsg) bool {
	m.mu.Lock()
	s := m.find(id)
	if s == nil {
		m.mu.Unlock()
		return false
	}
	s.Updated = time.Now()
	if msg.SessionID != "" {
		s.ClaudeID = msg.SessionID
	}
	u := Update{SessionID: id}
	done := false
	switch msg.Type {
	case "assistant", "user":
		for _, l := range describe(msg) {
			s.Lines = appendLine(s.Lines, l)
		}
	case "control_request":
		if msg.Request == nil || msg.Request.Subtype != "can_use_tool" {
			m.mu.Unlock()
			sub := ""
			if msg.Request != nil {
				sub = msg.Request.Subtype
			}
			_ = p.write(controlError(msg.RequestID, "unsupported control request: "+sub))
			return false
		}
		s.Pending = append(s.Pending, newRequest(id, msg))
		s.Status = StatusWaiting
		u.NeedsUser = true
	case "result":
		done = true
		s.CostUSD += msg.TotalCostUSD
		s.Result = msg.Result
		if msg.IsError {
			s.Status = StatusFailed
			s.Err = firstNonEmpty(msg.Result, msg.Subtype)
			s.Lines = appendLine(s.Lines, Line{LineError, "ended with error: " + s.Err})
		} else {
			s.Lines = appendLine(s.Lines, Line{LineDone, fmt.Sprintf("turn finished · $%.2f total", s.CostUSD)})
		}
	}
	m.mu.Unlock()
	m.notify(u)
	return done
}

func appendLine(lines []Line, l Line) []Line {
	if len(lines) >= maxLines {
		lines = append([]Line(nil), lines[len(lines)-maxLines/2:]...)
	}
	return append(lines, l)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// tailBuffer keeps the last few KB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > 8192 {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-4096:]...))
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
