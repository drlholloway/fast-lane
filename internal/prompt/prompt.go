// Package prompt renders the instructions given to Claude for each event.
package prompt

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/drlholloway/fast-lane/internal/config"
	"github.com/drlholloway/fast-lane/internal/github"
)

// System is appended to Claude's system prompt for every Fast Lane session.
const System = `You are running inside Fast Lane, a terminal app that hands you GitHub maintenance work while the user does other things.
The working directory is a local clone of the repository. Use the gh CLI for GitHub operations.
When you need a decision, use the AskUserQuestion tool; the user will answer in Fast Lane's dialog box.
Do not merge pull requests, push to the default branch, close issues, or dismiss alerts unless the user approves it through AskUserQuestion.
Finish with a short summary of what you did and anything left for the user.`

// Defaults holds the built-in template for each event kind.
var Defaults = map[config.EventKind]string{
	config.KindPullRequest: `Review pull request #{{.Number}} "{{.Title}}" in {{.Repo}} by @{{.Author}}.
URL: {{.URL}}
{{if .Branch}}Branch: {{.Branch}}
{{end}}
Use gh pr view and gh pr diff to read it, check it out locally if you need to run the build or tests, and check CI with gh pr checks.
Draft a review covering correctness, tests and risk. Ask me before submitting the review with gh pr review.

Description:
{{.Body}}`,

	config.KindIssue: `Triage issue #{{.Number}} "{{.Title}}" in {{.Repo}} opened by @{{.Author}}.
URL: {{.URL}}

Read the issue and its comments with gh issue view --comments, then find the relevant code.
If it is a bug, try to reproduce it. If the fix is small and clear, make it on a new branch with a test,
then ask me before pushing and opening a pull request. Otherwise suggest labels and a reply.

Issue body:
{{.Body}}`,

	config.KindDependabot: `Fix Dependabot alert #{{.Number}} in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Upgrade the dependency to a patched version on a new branch, update lockfiles, and run the build and tests.
Check whether Dependabot already opened a pull request for this first (gh pr list --author app/dependabot).
Ask me before pushing and opening a pull request.`,

	config.KindCodeScanning: `Fix code scanning alert #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Decide whether the finding is real. If it is, fix it on a new branch with a test and ask me before pushing and opening a pull request.
If it looks like a false positive, explain why and ask me whether to dismiss it.`,
}

// Render builds the prompt for ev, using the override in cfg when present.
func Render(cfg *config.Config, ev github.Event) (string, error) {
	src, ok := cfg.Prompts[ev.Kind]
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
