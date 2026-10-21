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
	if slices.Contains(allow, config.ActionComment) {
		b.WriteString("- Reply in a review thread with gh api --method POST repos/OWNER/REPO/pulls/N/comments/COMMENT_ID/replies -f body=...\n")
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

// Defaults holds the built-in template for each kind and some triggers.
// Triggers without one use their kind's template.
var Defaults = map[config.RuleKey]string{
	"pull_request": `Review pull request #{{.Number}} "{{.Title}}" in {{.Repo}} by @{{.Author}}.
URL: {{.URL}}
{{if .Branch}}Branch: {{.Branch}}
{{end}}
Use gh pr view and gh pr diff to read it, check it out locally if you need to run the build or tests, and check CI with gh pr checks.
Write a review covering correctness, tests and risk, and submit it with gh pr review.

Description:
{{.Body}}`,

	"issue": `Triage issue #{{.Number}} "{{.Title}}" in {{.Repo}} opened by @{{.Author}}.
URL: {{.URL}}

Read the issue and its comments with gh issue view --comments, then find the relevant code.
If it is a bug, try to reproduce it. If the fix is small and clear, make it on a new branch with a test,
push the branch and open a pull request. Otherwise add fitting labels and reply with what you found.

Issue body:
{{.Body}}`,

	"dependabot_alert": `Fix Dependabot alert #{{.Number}} in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Upgrade the dependency to a patched version on a new branch, update lockfiles, and run the build and tests.
Check whether Dependabot already opened a pull request for this first (gh pr list --author app/dependabot).
Then push the branch and open a pull request.`,

	"code_scanning_alert": `Fix code scanning alert #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}
Severity: {{.Severity}}

{{.Body}}

Decide whether the finding is real. If it is, fix it on a new branch with a test, push the branch and open a pull request.
If it looks like a false positive, explain why and dismiss it.`,

	"pull_request:pushed": `New commits were pushed to pull request #{{.Number}} "{{.Title}}" in {{.Repo}}{{if .Actor}} by @{{.Actor}}{{end}}; the head is now {{.HeadSHA}}.
URL: {{.URL}}

Look at what changed since the last review (gh pr diff, git log), then review the new commits and submit the review with gh pr review.`,

	"pull_request:checks_failed": `CI checks failed on pull request #{{.Number}} "{{.Title}}" in {{.Repo}} at {{.HeadSHA}}.
URL: {{.URL}}

Find the failing checks with gh pr checks and read their logs with gh run view --log-failed.
Check out the PR (gh pr checkout {{.Number}}), fix the cause, run the affected tests, and push the fix to the PR branch.
If the failure is unrelated to the PR (a flaky test, an outage), say so instead of changing code.`,

	"pull_request:conflict": `Pull request #{{.Number}} "{{.Title}}" in {{.Repo}} has merge conflicts with its base branch.
URL: {{.URL}}

Check out the PR (gh pr checkout {{.Number}}), merge the base branch into it, resolve the conflicts so both sides' intent survives,
run the build and tests, and push. Merge rather than rebase so the push does not need --force.`,

	"pull_request:changes_requested": `@{{.Actor}} requested changes on pull request #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}

Latest review:
{{.Detail}}

Read every review comment (gh pr view {{.Number}} --comments, gh api --method GET repos/{{.Repo}}/pulls/{{.Number}}/comments).
Check out the PR, make the requested changes, run the tests, and push to the PR branch.
Then reply to each review comment saying what you changed, or why you did not.`,

	"pull_request:commented": `@{{.Actor}} commented on pull request #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}

{{.Detail}}

Respond to it. If it asks for a change, check out the PR, make it, and push to the PR branch. If it asks a question, answer it with a reply.`,

	"pull_request:mentioned": `@{{.Actor}} mentioned you on pull request #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}

{{.Detail}}

Read the PR and its discussion (gh pr view {{.Number}} --comments, gh pr diff {{.Number}}) and the code involved, then reply to what they asked.
If they asked for a change and the PR is yours, make it on the PR branch and push. Otherwise answer in a reply.`,

	"issue:mentioned": `@{{.Actor}} mentioned you on issue #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}

{{.Detail}}

Read the thread with gh issue view {{.Number}} --comments and the code involved, then reply to what they asked.`,

	"issue:assigned": `You were assigned issue #{{.Number}} "{{.Title}}" in {{.Repo}}, opened by @{{.Author}}.
URL: {{.URL}}

Read the issue and its comments with gh issue view {{.Number}} --comments, then find the relevant code.
Fix it on a new branch with a test, push the branch and open a pull request that closes the issue.
If what is wanted is unclear, reply on the issue with your questions instead.

Issue body:
{{.Body}}`,

	"issue:commented": `@{{.Actor}} commented on issue #{{.Number}} "{{.Title}}" in {{.Repo}}.
URL: {{.URL}}

{{.Detail}}

Read the rest of the thread with gh issue view {{.Number}} --comments and respond: answer questions, and if the comment changes what is needed, update your work.`,
}

// Data is what prompt templates see: the event's fields plus the change
// that triggered the session.
type Data struct {
	github.Event
	Trigger config.Trigger
	// Actor is who caused the change, when known.
	Actor string
	// Detail is the comment or review text, the labels added, or the commit.
	Detail string
}

// Render builds the prompt for change c on ev. The first template that is
// set wins: override (usually a rule's prompt), then cfg's prompt and the
// built-in one for the kind and trigger, then for the kind alone.
func Render(cfg *config.Config, ev github.Event, c github.Change, override string) (string, error) {
	key, kindKey := config.Key(ev.Kind, c.Trigger), config.RuleKey(ev.Kind)
	src := override
	if src == "" {
		src = cfg.Prompts[key]
	}
	if src == "" {
		src = Defaults[key]
	}
	fallback := src == "" && key != kindKey
	if fallback {
		if src = cfg.Prompts[kindKey]; src == "" {
			src = Defaults[kindKey]
		}
	}
	if src == "" {
		return "", fmt.Errorf("no prompt template for %s", key)
	}
	tmpl, err := template.New(string(key)).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("prompt template for %s: %w", key, err)
	}
	var buf bytes.Buffer
	if fallback {
		// A trigger without its own template reuses the kind's; say why.
		fmt.Fprintf(&buf, "Triggered by: %s", c.Trigger.Short())
		if c.Detail != "" {
			fmt.Fprintf(&buf, " (%s)", c.Detail)
		}
		buf.WriteString("\n\n")
	}
	if err := tmpl.Execute(&buf, Data{Event: ev, Trigger: c.Trigger, Actor: c.Actor, Detail: c.Detail}); err != nil {
		return "", fmt.Errorf("prompt template for %s: %w", key, err)
	}
	return buf.String(), nil
}
