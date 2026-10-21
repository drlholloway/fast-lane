package prompt

import (
	"strings"
	"testing"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

func TestRenderDefaults(t *testing.T) {
	cfg := config.Default()
	for _, k := range config.AllKinds {
		out, err := Render(cfg, github.Event{Kind: k, Repo: "o/r", Number: 3, Title: "T", Body: "B"})
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if !strings.Contains(out, "o/r") || !strings.Contains(out, "#3") {
			t.Fatalf("%s: missing fields in %q", k, out)
		}
	}
}

func TestRenderOverride(t *testing.T) {
	cfg := config.Default()
	cfg.Prompts = map[config.EventKind]string{config.KindIssue: "fix {{.Repo}}#{{.Number}}"}
	out, err := Render(cfg, github.Event{Kind: config.KindIssue, Repo: "o/r", Number: 9})
	if err != nil || out != "fix o/r#9" {
		t.Fatalf("got %q, %v", out, err)
	}
	cfg.Prompts[config.KindIssue] = "{{.Nope"
	if _, err := Render(cfg, github.Event{Kind: config.KindIssue}); err == nil {
		t.Fatal("expected parse error")
	}
}
