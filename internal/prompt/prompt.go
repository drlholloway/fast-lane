// Package prompt renders the instructions given to Claude for each event.
package prompt

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// System returns the text appended to Claude's system prompt for a session
// that may take the allowed actions without asking.
func System(allow []config.Action) string {
	var may, ask []string
	for _, a := range config.AllActions {
		switch d := a.Describe(); {
		case d == "":
		case slices.Contains(allow, a):
			may = append(may, d)
		default:
			ask = append(ask, d)
		}
	}
	var b strings.Builder
	b.WriteString(`You are running inside Fast Lane, a terminal app that hands you GitHub maintenance work while the user does other things.
The working directory is a local clone of the repository. Use the gh CLI for GitHub operations.
Read from the GitHub API with gh api --method GET; other gh api calls need the user's approval unless an allowed action covers them.
When you need a decision, use the AskUserQuestion tool; the user will answer in Fast Lane's dialog box.
These rules override the task instructions:
`)
	if len(ask) > 0 {
		fmt.Fprintf(&b, "- Ask the user with AskUserQuestion before you %s.\n", list(ask, "or"))
	}
	if len(may) > 0 {
		fmt.Fprintf(&b, "- You may %s without asking.\n", list(may, "and"))
	}
	if slices.Contains(allow, config.ActionDismissAlert) {
		b.WriteString("- Dismiss alerts with gh api --method PATCH repos/OWNER/REPO/dependabot/alerts/N or .../code-scanning/alerts/N, giving the reason and a comment.\n")
	}
	b.WriteString(`- Always ask before you push to the default branch, force-push, or change anything outside this repository.
Finish with a short summary of what you did and anything left for the user.`)
	return b.String()
}

// list joins items as prose: "a", "a or b", "a, b or c".
func list(items []string, conj string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

// Defaults holds the built-in template for each event kind.
var Defaults = map[config.EventKind]string{
	config.KindPullRequest: `Review pull request #{{.Number}} "{{.Title}}" in {{.Repo}} by @{{.Author}}.
URL: {{.URL}}
{{if .Branch}}Branch: {{.Branch}}
{{end}}
Use gh pr view and gh pr diff to read it, check it out locally if you need to run the build or tests, and check CI with gh pr checks.
Write a review covering correctness, tests and risk, and submit it with gh pr review.

Description:
{{.Body}}`,

	config.KindIssue: `Triage issue #{{.Number}} "{{.Title}}" in {{.Repo}} opened by @{{.Author}}.
URL: {{.URL}}

Read the issue and its comments with gh issue view --comments, then find the relevant code.
If it is a bug, try to reproduce it. If the fix is small and clear, make it on a new branch with a test,
push the branch and open a pull request. Otherwise add fitting labels and reply with what you found.

Issue body:
{{.Body}}`,

	config.KindDependabot: `Fix Dependabot alert #{{.Number}} in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Upgrade the dependency to a patched version on a new branch, update lockfiles, and run the build and tests.
Check whether Dependabot already opened a pull request for this first (gh pr list --author app/dependabot).
Then push the branch and open a pull request.`,

	config.KindCodeScanning: `Fix code scanning alert #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Decide whether the finding is real. If it is, fix it on a new branch with a test, push the branch and open a pull request.
If it looks like a false positive, explain why and dismiss it.`,
}

// Render builds the prompt for ev. The first template that is set wins:
// override (usually a rule's prompt), cfg's prompt for the kind, the default.
func Render(cfg *config.Config, ev github.Event, override string) (string, error) {
	src, ok := override, override != ""
	if !ok {
		src, ok = cfg.Prompts[ev.Kind]
	}
	if !ok || src == "" {
		src, ok = Defaults[ev.Kind]
		if !ok {
			return "", fmt.Errorf("no prompt template for %s", ev.Kind)
		}
	}
	tmpl, err := template.New(string(ev.Kind)).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("prompt template for %s: %w", ev.Kind, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ev); err != nil {
		return "", fmt.Errorf("prompt template for %s: %w", ev.Kind, err)
	}
	return buf.String(), nil
}
