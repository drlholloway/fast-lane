# Fast Lane
## A TUI for monitoring and automating GitHub events

## Introduction

GitHub is an insanely useful tool, but some events just sit there waiting for work that could easily be automated. Fast Lane is a TUI that runs in your console. It watches the repositories you pick from the ones you have access to, and it handles the routine work you'd otherwise do by hand. It starts Claude Code sessions to deal with PRs, issues, and security alerts, so this maintenance doesn't interrupt your day.

## How it Works

It uses the GitHub credentials stored by the `gh` CLI to list your repositories, the repositories you've starred, and the ones where you have open PRs. You choose which repositories to monitor and which kinds of activity to watch. When something happens, you can start a Claude session to act on it, either by hand or automatically. Whenever a session needs your input, the TUI opens a dialog box where you can answer it. As long as the TUI is open, it keeps track of the work that has been done and automated.

## Requirements

- [GitHub CLI](https://cli.github.com), logged in (`gh auth login`)
- [Claude Code](https://claude.com/claude-code) on your `PATH`
- Go 1.26.7+ to build from source

## Install

```sh
go install github.com/drlholloway/fast-lane@latest
# or, from a checkout
go build -o fast-lane . && ./fast-lane
```

## Using it

The screen has three tabs. Switch between them with `1`/`2`/`3` or `tab`.

| Tab | What it shows | Keys |
| --- | --- | --- |
| **Events** | Open PRs, issues, Dependabot alerts and code scanning alerts on watched repos. A `●` marks an event that arrived since you started watching. | `enter` start Claude (you can edit the instructions first, then `ctrl+s`) · `s` jump to its session · `o` open in browser · `x` dismiss until it changes |
| **Sessions** | Every Claude session and its live transcript, with status and cost. | `enter` reply to a finished session or answer a pending prompt · `x` stop · `pgup`/`pgdn` scroll · `f` follow |
| **Repos** | Your repos, starred repos and repos where you have open PRs. | `space` watch/unwatch · `a` toggle auto-run · `e` choose event kinds and local checkout · `f` cycle filter · `/` search |

`R` refreshes everything, `?` shows more help, and `q` quits.

### When Claude needs you

Every session runs `claude -p` in streaming mode, and Fast Lane answers its permission prompts. When Claude wants to run a tool that isn't pre-approved, or calls `AskUserQuestion`, a dialog pops up (with a terminal bell):

- **Permission:** `y` allow, `a` allow that kind of action for the rest of the session, `n` deny (optionally telling Claude why), `esc` decide later.
- **Question:** pick an option with the arrows or number keys, or choose *Something else…* to type an answer.

When Claude finishes a turn, press `enter` on the session to reply. The conversation resumes where it left off, even after you restart the TUI.

### Safety defaults

- New repos are watched **without** auto-run. Turn it on per repo with `a`, and narrow it with `auto_rules` (see [Configuration](#configuration)). Events that already exist when you start watching a repo never auto-run, only ones that show up later.
- Claude runs with `--permission-mode manual`, and only read-only tools are pre-approved (`Read`, `Grep`, `Glob`, `git status/diff/log/show`, and `gh pr view/diff/checks`, `gh issue view`, and `gh api --method GET` for API reads). Anything else asks you first, including `gh api` writes.
- Claude's system prompt tells it to ask before pushing, opening PRs, commenting, labeling, submitting reviews, closing, merging or dismissing alerts, unless an auto rule's `allow` list pre-approves that action. It always asks before pushing to the default branch or force-pushing.
- Sessions for the same repo run one at a time because they share a working copy. At most `max_concurrent` sessions run overall.

## Configuration

Settings live in `~/.config/fast-lane/config.json` (or `$XDG_CONFIG_HOME`) and are written for you as you use the Repos tab. Use `-config <path>` to choose another file.

```json
{
  "poll_interval": "2m",
  "workspace_dir": "~/.local/share/fast-lane/workspaces",
  "max_concurrent": 2,
  "claude": {
    "path": "claude",
    "model": "",
    "permission_mode": "manual",
    "allowed_tools": ["Read", "Grep", "Glob", "Bash(gh pr view:*)"],
    "max_budget_usd": 5,
    "extra_args": []
  },
  "prompts": {
    "issue": "Triage issue #{{.Number}} in {{.Repo}}: {{.Title}}\n\n{{.Body}}"
  },
  "auto_rules": {
    "dependabot_alert": { "min_severity": "high", "allow": ["edit", "push_branch", "open_pr"] },
    "pull_request": { "skip_drafts": true, "skip_bots": true, "skip_authors": ["you"] }
  },
  "repos": [
    {
      "full_name": "you/your-repo",
      "local_path": "~/code/your-repo",
      "events": ["pull_request", "issue", "dependabot_alert", "code_scanning_alert"],
      "auto_run": false,
      "auto_rules": {
        "issue": {
          "labels": ["bug"],
          "skip_labels": ["wontfix"],
          "prompt": "Fix bug #{{.Number}} in {{.Repo}}: {{.Title}}\n\n{{.Body}}",
          "allow": ["edit", "comment"]
        }
      }
    }
  ]
}
```

- **`workspace_dir`**: repos without a `local_path` are cloned here with `gh repo clone` on first use and fetched before each new session.
- **`permission_mode`**: any mode `claude --permission-mode` accepts (`manual`, `acceptEdits`, `auto`, …). Prompts the mode doesn't settle on its own still come to the TUI.
- **`prompts`**: override the instructions for an event kind (an auto rule's `prompt` takes precedence) (`pull_request`, `issue`, `dependabot_alert`, `code_scanning_alert`). Templates use Go `text/template` and can use `.Repo`, `.Number`, `.Title`, `.URL`, `.Author`, `.Bot`, `.Labels`, `.Branch`, `.Severity`, `.Body`, `.Draft`, `.Kind`, `.CreatedAt` and `.UpdatedAt`. The defaults are in [`internal/prompt/prompt.go`](internal/prompt/prompt.go).
- **`auto_rules`**: choose which new events auto-run on repos with `auto_run` on. With no rules anywhere, every new event auto-runs. Once rules exist, only the kinds listed auto-run, and only events that pass every filter set on their kind's rule (an empty rule `{}` accepts every event of that kind). A repo's own `auto_rules` replace the global ones entirely. Events that don't match still show up in the feed for you to start by hand. Filters, all compared without regard to case:
  - `min_severity` (alerts only): `low`, `medium`, `high` or `critical`. Code scanning's `note`, `warning` and `error` rank with `low`, `medium` and `high`.
  - `labels` / `skip_labels` (PRs and issues only): require at least one of these labels / skip events with any of them.
  - `authors` / `skip_authors`: require / skip these logins.
  - `skip_drafts` (PRs only): skip draft pull requests.
  - `skip_bots`: skip events opened by bot accounts, Dependabot alerts included.

  A rule also shapes the sessions it covers, whether they auto-run or you start them with `enter`:
  - `prompt`: a template that replaces the kind's prompt (same fields as `prompts`).
  - `allow`: actions Claude may take without asking. Each one pre-approves the tools below for that session and tells Claude it needs no approval. Anything not allowed still comes to you as a question or permission dialog.

  | Action | Pre-approves |
  |---|---|
  | `edit` | `Edit`, `Write`, `git checkout/switch/add/commit` |
  | `push_branch` | `git push` of new branches (never the default branch, never with force) |
  | `open_pr` | `gh pr create` |
  | `update_pr` | `gh pr checkout`, `git push`, `gh pr edit`, `gh pr ready` |
  | `comment` | `gh issue comment`, `gh pr comment` |
  | `label` | `gh issue edit`, `gh pr edit` |
  | `review` | `gh pr review` |
  | `close` | `gh issue close`, `gh pr close` |
  | `merge` | `gh pr merge` |
  | `dismiss_alert` | `gh api --method PATCH` on this repo's Dependabot and code scanning alerts |

Runtime state lives in `~/.local/state/fast-lane/` (or `$XDG_STATE_HOME`): seen and dismissed events, session history in `state.json`, and the raw stream-json log of each session in `logs/`.

## Development

```sh
go test -race ./...
```

| Package | Role |
| --- | --- |
| `internal/github` | REST client built on `go-gh`, so it uses `gh`'s auth |
| `internal/claude` | Session manager: spawns `claude`, parses stream-json and answers `can_use_tool` control requests |
| `internal/tui` | Bubble Tea interface |
| `internal/config`, `internal/state`, `internal/prompt` | Settings, persistence and prompt templates |

## License

Fast Lane is licensed under the [PolyForm Shield License 1.0.0](LICENSE.md). You're free to use, modify and share it, including at work. What you can't do is use it to offer a product that competes with Fast Lane, such as selling it or a derivative of it as a product or service.

Because of that restriction, this is a *source-available* license rather than an OSI-approved open source license.
