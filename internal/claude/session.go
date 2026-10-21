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
	mu       sync.Mutex
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
	// running tracks execute goroutines so Shutdown can wait for them.
	running sync.WaitGroup
}

// NewManager creates a manager that writes raw session logs to logDir.
func NewManager(cfg *config.Config, logDir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:     cfg,
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
	text, err := prompt.Render(m.cfg, ev, github.Opened(ev), "")
	if err != nil {
		return 0, err
	}
	return m.StartPrompt(ev, text, nil), nil
}

// StartPrompt queues a new session for ev with custom instructions and the
// actions Claude may take without asking, and returns its ID.
func (m *Manager) StartPrompt(ev github.Event, text string, allow []config.Action) int {
	m.mu.Lock()
	now := time.Now()
	s := &Session{ID: m.nextID, Event: ev, Status: StatusQueued, Started: now, Updated: now, Allow: allow, nextInput: text}
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
	return m.resume(id, text, nil, false)
}

// Resume is Reply for a trigger: it also replaces the actions Claude may
// take without asking.
func (m *Manager) Resume(id int, text string, allow []config.Action) error {
	return m.resume(id, text, allow, true)
}

func (m *Manager) resume(id int, text string, allow []config.Action, setAllow bool) error {
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
	if setAllow {
		s.Allow = allow
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

func (m *Manager) repoBusy(repo string) bool {
	for id := range m.procs {
		if s := m.find(id); s != nil && strings.EqualFold(s.Event.Repo, repo) {
			return true
		}
	}
	return false
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
		if m.repoBusy(s.Event.Repo) {
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
	repo, number, resume, allow := s.Event.Repo, s.Event.Number, s.ClaudeID, s.Allow
	m.mu.Unlock()

	dir := m.cfg.WorkDir(repo)
	if resume == "" {
		if err := m.prepareWorkdir(id, repo, dir); err != nil {
			m.fail(id, fmt.Sprintf("preparing %s: %v", dir, err))
			return
		}
	}

	err := m.runClaude(id, p, dir, repo, number, resume, allow, input)
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

func (m *Manager) claudeArgs(repo string, number int, resume string, allow []config.Action) ([]string, error) {
	c := m.cfg.Claude
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
		"--append-system-prompt", prompt.System(allow),
	}
	if c.PermissionMode != "" {
		args = append(args, "--permission-mode", c.PermissionMode)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
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

func (m *Manager) runClaude(id int, p *proc, dir, repo string, number int, resume string, allow []config.Action, input string) error {
	args, err := m.claudeArgs(repo, number, resume, allow)
	if err != nil {
		return err
	}
	cmd := m.run(m.ctx, dir, m.cfg.Claude.Path, args...)
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
		return fmt.Errorf("start %s: %w", m.cfg.Claude.Path, err)
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
