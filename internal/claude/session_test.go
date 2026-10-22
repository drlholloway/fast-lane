package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
	"github.com/drlholloway/fast-lane/internal/local"
)

// TestHelperProcess plays the part of `claude -p` in stream-json mode. It
// asks permission for a Write, echoes the decision, and finishes the turn.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("FASTLANE_HELPER") != "1" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }

	in.Scan() // user message
	var first map[string]any
	_ = json.Unmarshal(in.Bytes(), &first)
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "sess-1"})
	// FASTLANE_HELPER_BASH makes it ask to run that shell command instead.
	tool, input := "Write", map[string]any{"file_path": "x.txt"}
	if c := os.Getenv("FASTLANE_HELPER_BASH"); c != "" {
		tool, input = "Bash", map[string]any{"command": c}
	}
	emit(map[string]any{"type": "assistant", "session_id": "sess-1", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "name": tool, "input": input}},
	}})
	emit(map[string]any{"type": "control_request", "request_id": "r1", "request": map[string]any{
		"subtype": "can_use_tool", "tool_name": tool, "input": input,
	}})
	in.Scan()
	var resp struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
			} `json:"response"`
		} `json:"response"`
	}
	_ = json.Unmarshal(in.Bytes(), &resp)
	emit(map[string]any{"type": "assistant", "session_id": "sess-1", "message": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "decision=" + resp.Response.Response.Behavior + " id=" + resp.Response.RequestID}},
	}})
	emit(map[string]any{"type": "result", "subtype": "success", "session_id": "sess-1", "result": "ok", "total_cost_usd": 0.25})
	os.Exit(0)
}

var callsMu sync.Mutex

func fakeRunner(t *testing.T, calls *[][]string) Runner {
	return func(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
		callsMu.Lock()
		*calls = append(*calls, append([]string{name}, args...))
		callsMu.Unlock()
		if name != "claude" {
			// git/gh: succeed without doing anything.
			return exec.CommandContext(ctx, "true")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
		cmd.Env = append(os.Environ(), "FASTLANE_HELPER=1")
		cmd.Dir = dir
		return cmd
	}
}

func waitFor(t *testing.T, m *Manager, id int, cond func(Session) bool) Session {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, s := range m.Snapshot() {
			if s.ID == id && cond(s) {
				return s
			}
		}
		select {
		case <-m.Updates():
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out; sessions: %+v", m.Snapshot())
		}
	}
}

func TestSessionPermissionRoundTrip(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceDir = t.TempDir()
	repoDir := cfg.CloneDir("o/r")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	m := NewManager(cfg, t.TempDir())
	m.SetRunner(fakeRunner(t, &calls))
	defer m.Shutdown()

	id, err := m.Start(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 1, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if s.Status != StatusWaiting || s.Pending[0].ToolName != "Write" || s.Pending[0].Summary != "x.txt" {
		t.Fatalf("unexpected pending state: %+v", s)
	}
	if err := m.Respond(s.Pending[0], Answer{Allow: true}); err != nil {
		t.Fatal(err)
	}
	s = waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })
	if s.ClaudeID != "sess-1" || s.CostUSD != 0.25 {
		t.Fatalf("got %+v", s)
	}
	var texts []string
	for _, l := range s.Lines {
		texts = append(texts, l.Text)
	}
	if joined := strings.Join(texts, "\n"); !strings.Contains(joined, "decision=allow id=r1") {
		t.Fatalf("transcript missing decision:\n%s", joined)
	}

	// A reply resumes the same Claude session.
	if err := m.Reply(id, "thanks"); err != nil {
		t.Fatal(err)
	}
	s = waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if err := m.Respond(s.Pending[0], Answer{Allow: false, Message: "no"}); err != nil {
		t.Fatal(err)
	}
	s = waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle && s.CostUSD == 0.5 })

	callsMu.Lock()
	defer callsMu.Unlock()
	last := calls[len(calls)-1]
	if !contains(last, "--resume") || !contains(last, "sess-1") {
		t.Fatalf("reply did not resume: %v", last)
	}
	for _, c := range calls {
		if c[0] == "git" || c[0] == "gh" {
			continue
		}
		if !contains(c, "--permission-prompt-tool") {
			t.Fatalf("missing stdio permission tool: %v", c)
		}
	}
}

func TestClaudeArgsAddAllowedActions(t *testing.T) {
	m := NewManager(config.Default(), "")
	defer m.Shutdown()
	args, err := m.claudeArgs(m.cfg.Claude, "o/r", 3, "", []config.Action{config.ActionPushBranch, config.ActionOpenPR, config.ActionUpdatePR, config.ActionDismissAlert, config.ActionComment}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	flag := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		t.Fatalf("missing %s in %v", name, args)
		return ""
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(flag("--settings")), &settings); err != nil {
		t.Fatal(err)
	}
	allow := settings.Permissions.Allow
	if !contains(allow, "Read") || !contains(allow, "Bash(git push:*)") || !contains(allow, "Bash(gh pr create:*)") {
		t.Fatalf("allowed tools should combine config and actions: %v", allow)
	}
	if !contains(allow, "Bash(gh api --method PATCH repos/o/r/dependabot/alerts/:*)") {
		t.Fatalf("dismiss_alert should be scoped to the session's repo: %v", allow)
	}
	if !contains(allow, "Bash(gh api --method POST repos/o/r/pulls/3/comments:*)") {
		t.Fatalf("thread replies should be scoped to the session's PR: %v", allow)
	}
	if contains(allow, "Bash(gh api:*)") {
		t.Fatalf("gh api writes must not be pre-approved by default: %v", allow)
	}
	if strings.Count(strings.Join(allow, "\n"), "Bash(git push:*)") != 1 {
		t.Fatalf("duplicate tool rules: %v", allow)
	}
	for _, a := range args {
		if a == "--effort" {
			t.Fatal("no --effort without an effort setting")
		}
	}
	c := m.cfg.Claude
	c.Effort = "medium"
	withEffort, _ := m.claudeArgs(c, "o/r", 3, "", nil, false, false)
	if i := slices.Index(withEffort, "--effort"); i < 0 || withEffort[i+1] != "medium" {
		t.Fatalf("effort should be passed on: %v", withEffort)
	}
	if !strings.Contains(flag("--append-system-prompt"), "You may push new branches, open pull requests, update existing pull requests") {
		t.Fatal("system prompt should list the allowed actions")
	}
}

func TestQueueSerializesSameRepo(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConcurrent = 5
	cfg.WorkspaceDir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg.CloneDir("o/r"), ".git"), 0o755)
	var calls [][]string
	m := NewManager(cfg, "")
	m.SetRunner(fakeRunner(t, &calls))
	defer m.Shutdown()

	a, _ := m.Start(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 1})
	b, _ := m.Start(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 2})
	waitFor(t, m, a, func(s Session) bool { return s.Status == StatusWaiting })
	for _, s := range m.Snapshot() {
		if s.ID == b && s.Status != StatusQueued {
			t.Fatalf("second session on same repo should queue, got %s", s.Status)
		}
	}
	m.Stop(a)
	waitFor(t, m, a, func(s Session) bool { return s.Status == StatusStopped })
	waitFor(t, m, b, func(s Session) bool { return s.Status == StatusWaiting })
}

func TestControlResponseQuestion(t *testing.T) {
	req := Request{RequestID: "q", ToolName: "AskUserQuestion", Input: json.RawMessage(`{"questions":[{"question":"Color?"}]}`)}
	b, err := controlResponse(req, Answer{Allow: true, Answers: map[string]string{"Color?": "Blue"}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Response struct {
			Response struct {
				Behavior     string         `json:"behavior"`
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	ans := got.Response.Response.UpdatedInput["answers"].(map[string]any)
	if got.Response.Response.Behavior != "allow" || ans["Color?"] != "Blue" {
		t.Fatalf("got %s", b)
	}
	if _, ok := got.Response.Response.UpdatedInput["questions"]; !ok {
		t.Fatal("original input must be preserved")
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// gitRunner runs git for real and plays claude with the helper process.
func gitRunner(t *testing.T) Runner {
	return func(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
		switch name {
		case "git":
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = dir
			return cmd
		case "claude":
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
			cmd.Env = append(os.Environ(), "FASTLANE_HELPER=1")
			cmd.Dir = dir
			return cmd
		}
		return exec.CommandContext(ctx, "true")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestWorktreeFromLocalCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote, checkout := filepath.Join(root, "remote.git"), filepath.Join(root, "checkout")
	git(t, root, "init", "--quiet", "--bare", "-b", "main", remote)
	git(t, root, "init", "--quiet", "-b", "main", checkout)
	os.WriteFile(filepath.Join(checkout, "a.txt"), []byte("one\n"), 0o644)
	git(t, checkout, "add", ".")
	git(t, checkout, "commit", "--quiet", "-m", "first")
	git(t, checkout, "remote", "add", "origin", remote)
	git(t, checkout, "push", "--quiet", "origin", "main")
	// The user is mid-work: on another branch with an uncommitted change.
	git(t, checkout, "switch", "--quiet", "-c", "my-work")
	os.WriteFile(filepath.Join(checkout, "a.txt"), []byte("unsaved\n"), 0o644)

	cfg := config.Default()
	cfg.WorkspaceDir = filepath.Join(root, "ws")
	cfg.MaxConcurrent = 5
	m := NewManager(cfg, "")
	m.SetRunner(gitRunner(t))
	m.SetCheckouts(map[string]local.Checkout{"o/r": {Dir: checkout, Remote: "origin"}})
	defer m.Shutdown()

	a, _ := m.Start(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 1})
	b, _ := m.Start(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 2})
	sa := waitFor(t, m, a, func(s Session) bool { return s.Status == StatusWaiting })
	waitFor(t, m, b, func(s Session) bool { return s.Status == StatusWaiting })

	if sa.Dir != cfg.WorktreeDir("o/r", a) || sa.Checkout != checkout {
		t.Fatalf("session should run in its own worktree: %+v", sa)
	}
	if got := git(t, sa.Dir, "rev-parse", "HEAD"); got != git(t, checkout, "rev-parse", "origin/main") {
		t.Fatalf("worktree should start at the remote's default branch, at %s", got)
	}
	if git(t, checkout, "branch", "--show-current") != "my-work" || git(t, checkout, "status", "--porcelain") != "M a.txt" {
		t.Fatal("the user's checkout must be left exactly as it was")
	}

	if err := m.Respond(sa.Pending[0], Answer{Allow: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, a, func(s Session) bool { return s.Status == StatusIdle })

	// Pruning removes the worktree but keeps the session; resuming makes it again.
	if n, err := m.PruneWorktrees(0); err != nil || n != 1 {
		t.Fatalf("prune: %d, %v", n, err)
	}
	if _, err := os.Stat(sa.Dir); !os.IsNotExist(err) {
		t.Fatal("worktree should be gone")
	}
	if err := m.Reply(a, "again"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, a, func(s Session) bool { return s.Status == StatusWaiting })
	if _, err := os.Stat(filepath.Join(sa.Dir, ".git")); err != nil {
		t.Fatal("resuming should recreate the worktree at the same path")
	}

	m.Stop(a)
	waitFor(t, m, a, func(s Session) bool { return s.Status == StatusStopped })
	if err := m.Delete(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sa.Dir); !os.IsNotExist(err) {
		t.Fatal("delete should remove the worktree")
	}
	for _, s := range m.Snapshot() {
		if s.ID == a {
			t.Fatal("delete should remove the session")
		}
	}
	if err := m.Delete(b); err == nil {
		t.Fatal("a working session cannot be deleted")
	}
}

func TestOldSessionsResumeWhereTheyStarted(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceDir = t.TempDir()
	m := NewManager(cfg, "")
	m.SetCheckouts(map[string]local.Checkout{"o/r": {Dir: "/src/r", Remote: "origin"}})
	defer m.Shutdown()
	s := &Session{ID: 3, Event: github.Event{Repo: "o/r"}, ClaudeID: "c-3"}
	m.assignDir(s)
	if s.Dir != cfg.CloneDir("o/r") || s.Checkout != "" {
		t.Fatalf("a session from before worktrees must resume in its old directory: %+v", s)
	}
}

func modelArg(call []string) string {
	for i, a := range call {
		if a == "--model" && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

func lastClaudeCall(calls [][]string) []string {
	callsMu.Lock()
	defer callsMu.Unlock()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i][0] == "claude" {
			return calls[i]
		}
	}
	return nil
}

func TestSessionModel(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceDir = t.TempDir()
	cfg.Claude.Model = "claude-sonnet-5-5"
	os.MkdirAll(filepath.Join(cfg.CloneDir("o/r"), ".git"), 0o755)
	var calls [][]string
	m := NewManager(cfg, "")
	m.SetRunner(fakeRunner(t, &calls))
	defer m.Shutdown()

	id := m.StartPrompt(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 1}, "go", Options{Model: "claude-haiku-5-5"})
	s := waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if got := modelArg(lastClaudeCall(calls)); got != "claude-haiku-5-5" || s.Model != "claude-haiku-5-5" {
		t.Fatalf("the session's model should replace the default: %q", got)
	}
	m.Respond(s.Pending[0], Answer{Allow: true})
	waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })

	// A plain reply keeps the session's model.
	m.Reply(id, "more")
	s = waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if got := modelArg(lastClaudeCall(calls)); got != "claude-haiku-5-5" {
		t.Fatalf("a reply should keep the model: %q", got)
	}
	m.Respond(s.Pending[0], Answer{Allow: true})
	waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })

	// A trigger whose rule sets no model falls back to the default.
	m.Resume(id, "ci failed", Options{})
	waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if got := modelArg(lastClaudeCall(calls)); got != "claude-sonnet-5-5" {
		t.Fatalf("resuming under a rule without a model should use the default: %q", got)
	}
}

func effortArg(call []string) string {
	if i := slices.Index(call, "--effort"); i >= 0 && i+1 < len(call) {
		return call[i+1]
	}
	return ""
}

func TestSessionEffort(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceDir = t.TempDir()
	cfg.Claude.Effort = "medium"
	os.MkdirAll(filepath.Join(cfg.CloneDir("o/r"), ".git"), 0o755)
	var calls [][]string
	m := NewManager(cfg, "")
	m.SetRunner(fakeRunner(t, &calls))
	defer m.Shutdown()

	id := m.StartPrompt(github.Event{Repo: "o/r", Kind: config.KindIssue, Number: 1}, "go", Options{Effort: "high"})
	s := waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if got := effortArg(lastClaudeCall(calls)); got != "high" || s.Effort != "high" {
		t.Fatalf("the session's effort should replace the default: %q", got)
	}
	m.Respond(s.Pending[0], Answer{Allow: true})
	waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })

	m.Resume(id, "again", Options{})
	waitFor(t, m, id, func(s Session) bool { return len(s.Pending) == 1 })
	if got := effortArg(lastClaudeCall(calls)); got != "medium" {
		t.Fatalf("a rule without an effort should fall back to the default: %q", got)
	}
}

func fullAutoManager(t *testing.T, bash string) (*Manager, *[][]string) {
	t.Helper()
	cfg := config.Default()
	cfg.WorkspaceDir = t.TempDir()
	cfg.Claude.PermissionMode = "bypassPermissions"
	os.MkdirAll(filepath.Join(cfg.CloneDir("o/r"), ".git"), 0o755)
	var calls [][]string
	m := NewManager(cfg, "")
	run := fakeRunner(t, &calls)
	m.SetRunner(func(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
		cmd := run(ctx, dir, name, args...)
		if name == "claude" && bash != "" {
			cmd.Env = append(cmd.Env, "FASTLANE_HELPER_BASH="+bash)
		}
		return cmd
	})
	t.Cleanup(m.Shutdown)
	return m, &calls
}

func transcript(s Session) string {
	var b strings.Builder
	for _, l := range s.Lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func TestFullAutoAllowsOnItsOwn(t *testing.T) {
	m, calls := fullAutoManager(t, "gh pr merge 12 --squash")
	id := m.StartPrompt(github.Event{Repo: "o/r", Kind: config.KindPullRequest, Number: 12}, "go", Options{FullAuto: true})
	s := waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })
	if len(s.Pending) != 0 || !strings.Contains(transcript(s), "decision=allow") {
		t.Fatalf("full auto should approve without asking:\n%s", transcript(s))
	}
	call := lastClaudeCall(*calls)
	if i := slices.Index(call, "--permission-mode"); i < 0 || call[i+1] != "manual" {
		t.Fatalf("full auto must not let a permission mode skip the guard: %v", call)
	}
}

func TestFullAutoRefusesWhatOnlyAHumanMayDo(t *testing.T) {
	m, _ := fullAutoManager(t, "gh repo delete o/r --yes")
	var refused string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range m.Updates() {
			if u.Refused != "" {
				refused = u.Refused
				return
			}
		}
	}()
	id := m.StartPrompt(github.Event{Repo: "o/r", Kind: config.KindPullRequest, Number: 12}, "go", Options{FullAuto: true})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no refusal reported")
	}
	s := waitFor(t, m, id, func(s Session) bool { return s.Status == StatusIdle })
	if !strings.Contains(transcript(s), "decision=deny") || !strings.Contains(transcript(s), "refused Bash: gh repo delete") {
		t.Fatalf("the guard should refuse and say so:\n%s", transcript(s))
	}
	if !strings.Contains(refused, "gh repo delete") {
		t.Fatalf("the TUI should hear about the refusal: %q", refused)
	}
}

func TestFullAutoArgs(t *testing.T) {
	m := NewManager(config.Default(), "")
	defer m.Shutdown()
	c := m.cfg.Claude
	c.PermissionMode = "auto"
	args, _ := m.claudeArgs(c, "o/r", 3, "", []config.Action{config.ActionPushBranch}, false, true)
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	json.Unmarshal([]byte(args[slices.Index(args, "--settings")+1]), &settings)
	for _, a := range settings.Permissions.Allow {
		if strings.HasPrefix(a, "Bash") {
			t.Fatalf("no shell command may be pre-approved in full auto: %v", settings.Permissions.Allow)
		}
	}
	if !contains(settings.Permissions.Allow, "Read") || !contains(settings.Permissions.Deny, "Bash(gh repo delete:*)") {
		t.Fatalf("got %+v", settings.Permissions)
	}
	if args[slices.Index(args, "--permission-mode")+1] != "manual" ||
		!strings.Contains(args[slices.Index(args, "--append-system-prompt")+1], "full auto mode") {
		t.Fatalf("got %v", args)
	}
}
