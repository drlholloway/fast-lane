package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
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
	emit(map[string]any{"type": "assistant", "session_id": "sess-1", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "name": "Write", "input": map[string]any{"file_path": "x.txt"}}},
	}})
	emit(map[string]any{"type": "control_request", "request_id": "r1", "request": map[string]any{
		"subtype": "can_use_tool", "tool_name": "Write", "input": map[string]any{"file_path": "x.txt"},
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
	repoDir := cfg.WorkDir("o/r")
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

func TestQueueSerializesSameRepo(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConcurrent = 5
	cfg.WorkspaceDir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg.WorkDir("o/r"), ".git"), 0o755)
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
