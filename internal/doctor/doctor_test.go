package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

type fakeGH struct {
	github.Client
	auth     github.Auth
	authErr  error
	inboxErr error
}

func (f fakeGH) Auth(context.Context) (github.Auth, error) { return f.auth, f.authErr }
func (f fakeGH) Inbox(context.Context, string, time.Time) ([]github.InboxItem, error) {
	return nil, f.inboxErr
}

func env(t *testing.T, gh fakeGH, versions map[string]string) Env {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "cfg", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkspaceDir = filepath.Join(dir, "ws")
	code := filepath.Join(dir, "code")
	os.MkdirAll(filepath.Join(code, "app", ".git"), 0o755)
	os.WriteFile(filepath.Join(code, "app", ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:me/app.git\n"), 0o644)
	cfg.CodeDirs = []string{code, filepath.Join(dir, "missing")}
	return Env{
		Config: cfg,
		GitHub: gh,
		LookPath: func(bin string) (string, error) {
			if _, ok := versions[bin]; !ok {
				return "", errors.New("not found")
			}
			return "/bin/" + bin, nil
		},
		Output: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			return []byte(versions[filepath.Base(name)] + "\nmore\n"), nil
		},
	}
}

func byName(cs []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range cs {
		out[c.Name] = c
	}
	return out
}

var allGood = map[string]string{"gh": "gh version 2.80.0", "claude": "2.1.0 (Claude Code)", "git": "git version 2.47.1"}

func TestAllGood(t *testing.T) {
	gh := fakeGH{auth: github.Auth{Login: "me", Scopes: []string{"repo", "read:org", "gist"}, ScopesKnown: true}}
	cs := Run(context.Background(), env(t, gh, allGood))
	if w := Worst(cs); w != OK {
		t.Fatalf("expected all OK: %+v", cs)
	}
	got := byName(cs)
	if got["Claude Code"].Detail != "2.1.0 (Claude Code)" || !strings.Contains(got["GitHub login"].Detail, "@me") {
		t.Fatalf("details: %+v", cs)
	}
	if !strings.Contains(got["Code folders"].Detail, "(1 checkout)") || strings.Contains(got["Code folders"].Detail, "missing") {
		t.Fatalf("code folders: %q", got["Code folders"].Detail)
	}
}

func TestProblems(t *testing.T) {
	gh := fakeGH{auth: github.Auth{Login: "me", Scopes: []string{"gist"}, ScopesKnown: true}, inboxErr: errors.New("403")}
	versions := map[string]string{"gh": "gh version 2.80.0", "git": "git version 2.16.0"}
	e := env(t, gh, versions)
	e.Config.CodeDirs = []string{"/nope"}
	got := byName(Run(context.Background(), e))

	for name, want := range map[string]Status{
		"Claude Code": Fail, "git": Fail, "GitHub login": Warn, "Notifications": Warn, "Code folders": Warn, "GitHub CLI": OK,
	} {
		if got[name].Status != want {
			t.Errorf("%s: got %d, want %d (%+v)", name, got[name].Status, want, got[name])
		}
		if want != OK && got[name].Fix == "" {
			t.Errorf("%s: a problem needs a fix", name)
		}
	}
	if !strings.Contains(got["GitHub login"].Fix, "gh auth refresh -s repo,read:org") {
		t.Errorf("fix should name the missing scopes: %q", got["GitHub login"].Fix)
	}
}

func TestNotLoggedIn(t *testing.T) {
	e := env(t, fakeGH{authErr: errors.New("HTTP 401")}, allGood)
	got := byName(Run(context.Background(), e))
	if got["GitHub login"].Status != Fail || got["Notifications"].Status != Fail {
		t.Fatalf("got %+v", got)
	}
	e.Config.Inbox.Enabled = false
	if _, ok := byName(Run(context.Background(), e))["Notifications"]; ok {
		t.Fatal("notifications are not checked with the inbox off")
	}
}
