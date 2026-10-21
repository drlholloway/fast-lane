# Fast Lane
## A TUI for monitoring and automating GitHub events

📖 **[Read the wiki](https://github.com/drlholloway/fast-lane/wiki)** for guides to every feature, ready-made [recipes](https://github.com/drlholloway/fast-lane/wiki/Recipes) and [troubleshooting](https://github.com/drlholloway/fast-lane/wiki/Troubleshooting).

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

## Getting started

1. **Start it.** Run `./fast-lane`, or `fast-lane` if you used `go install`. You need the GitHub CLI logged in (`gh auth login`), and Fast Lane uses it and Claude Code as they are, with no keys or tokens of its own. It loads your repos from GitHub and scans your code folders for local checkouts.

2. **Check your setup.** Press `5` for the Settings tab. Its Prerequisites section checks the GitHub CLI and your login's scopes, access to your notifications, Claude Code, git, the workspace and your code folders, and says how to fix anything that's off. If something needs attention at startup, the status line tells you. Below the checks you can change every setting, including Code folders if you keep code somewhere other than the [usual places](#local-checkouts).

3. **Watch a repo.** Press `3` for the Repos tab, `/` to search, and `space` to watch one. `e` picks which kinds of activity to watch. Its open PRs, issues and alerts show up on the Events tab (`1`). This first poll only records what's already open, so nothing older than now ever runs on its own.

4. **Run Claude by hand once.** On the Events tab, pick an item and press `enter`. You'll see the instructions Claude will get, and you can edit them before pressing `ctrl+s`. Follow along on the Sessions tab (`2`). When Claude wants to do something that isn't pre-approved, or asks a question, a dialog pops up: `y` allows, `n` denies, `a` allows that kind of action for the rest of the session. When it's done, press `enter` on the session to reply and keep going.

5. **Turn on automation for a repo.** On the Repos tab, `a` toggles auto-run. With no rules, every newly opened item on that repo starts a session by itself. To be choosier, open the Rules tab (`4`) and press `n`. For example, to have Claude fix CI on your own PRs:
   - set the kind to `pull_request` and the trigger to `checks_failed`
   - put your login under Authors
   - check `edit` and `update_pr` under Allow
   - press `ctrl+s`

   Once any rule exists, only what the rules list auto-runs. See [Triggers](#triggers) for everything Fast Lane can react to, and the `auto_rules` [configuration](#configuration) for every filter and action.

6. **Let your inbox come to you.** Review requests, @-mentions and assignments on any repo are picked up from your GitHub notifications and handled by default. Claude prepares the review or reply, then asks before posting it. `i` on the Rules tab switches this between running Claude, only listing them, and off. See [Inbox](#inbox).

7. **Leave it running.** Fast Lane polls every two minutes and rings the terminal bell when something needs you. What it has seen and the session history are saved, so you can quit with `q` and pick up later. Sessions that were mid-turn when you quit are marked interrupted, and you can resume them with a reply.

Claude starts out cautious. It can read code and GitHub freely, but it asks before editing files, pushing, commenting, reviewing, merging or anything else that changes something, until a rule's `allow` list says otherwise. It always works in its own git worktree or clone, never in your checkout. See [Safety defaults](#safety-defaults).

## Using it

The screen has five tabs. Switch between them with `1`–`5` or `tab`.

| Tab | What it shows | Keys |
| --- | --- | --- |
| **Events** | Open PRs, issues, Dependabot alerts and code scanning alerts on watched repos. A `●` marks an item that's new or changed since you last acted on it, and a tag such as `[CI failed]` says what changed. | `enter` start Claude (you can edit the instructions first, then `ctrl+s`) · `s` jump to its session · `o` open in browser · `x` dismiss until it changes |
| **Sessions** | Every Claude session and its live transcript, with status and cost. | `enter` reply to a finished session or answer a pending prompt · `x` stop · `D` delete a finished session and its worktree · `pgup`/`pgdn` scroll · `f` follow |
| **Repos** | Your repos, starred repos and repos where you have open PRs. | `space` watch/unwatch · `a` toggle auto-run · `e` choose event kinds and local checkout · `r` edit its rules · `f` cycle filter · `/` search |
| **Rules** | The [auto rules](#configuration) for the global scope or one watched repo, in the order they're tried, plus the inbox and the repo's auto-run setting. Changes are saved to the config file and apply from the next poll. | `[`/`]` switch scope · `n` new rule · `enter` edit · `d` delete · `J`/`K` reorder · `c` copy the global rules into this repo · `a` toggle the repo's auto-run · `i` cycle the inbox (auto-run, list only, off) |
| **Settings** | Prerequisite checks with fixes, then every top-level setting: polling, concurrency, the workspace, code folders, Claude Code's path, model, effort, permission mode, budget, allowed tools and extra arguments, the inbox, and the default prompt for each kind and trigger. An About section shows the version (also printed by `fast-lane -version`) and links to the source and to [buy me a coffee](https://buymeacoffee.com/drlholloway). | `enter` edit or cycle the selected setting, or open a link · `c` run the checks again |

`R` refreshes everything, `?` shows more help, and `q` quits.

### When Claude needs you

Every session runs `claude -p` in streaming mode, and Fast Lane answers its permission prompts. When Claude wants to run a tool that isn't pre-approved, or calls `AskUserQuestion`, a dialog pops up (with a terminal bell):

- **Permission:** `y` allow, `a` allow that kind of action for the rest of the session, `n` deny (optionally telling Claude why), `esc` decide later.
- **Question:** pick an option with the arrows or number keys, or choose *Something else…* to type an answer.

When Claude finishes a turn, press `enter` on the session to reply. The conversation resumes where it left off, even after you restart the TUI.

### Triggers

Every poll compares each open item with what Fast Lane saw last time. A change that matters is a *trigger*, and it can start a Claude session, or resume the item's last one so Claude keeps what it already learned. If that session is still working, the trigger waits until it finishes its turn.

| Trigger | Kinds | Fires when |
| --- | --- | --- |
| `opened` | all | An item shows up after you started watching the repo |
| `changes_requested` | PRs | Someone submits a review requesting changes |
| `checks_failed` | PRs | CI fails on the head commit |
| `conflict` | PRs | The PR picks up merge conflicts with its base |
| `commented` | PRs, issues | Someone comments, or leaves a review with comments |
| `review_requested` | PRs | Your review is requested, on any repo (see [Inbox](#inbox)) |
| `mentioned` | PRs, issues | Someone @-mentions you, on any repo |
| `assigned` | PRs, issues | You're assigned, on any repo |
| `ready_for_review` | PRs | A draft is marked ready |
| `pushed` | PRs | Someone else pushes new commits |
| `labeled` | PRs, issues | Labels are added |

When one poll finds several changes on an item, Fast Lane acts on the first one in this order that has an auto rule. Your own activity never triggers anything (Claude acts as you, so its pushes and comments don't loop back), and neither do comments from bots. Each change fires once: checks failing on one commit, or one particular comment.

### Inbox

Fast Lane also reads your GitHub notifications for review requests, @-mentions (including team mentions) and assignments, on any repository you can see, not just the ones you watch. Before acting on one, it checks that it still applies: the PR or issue is still open, your review is still requested, the comment really mentions you, you're still assigned. Items from repos you don't watch show up in the Events tab too, and sessions clone the repo on first use. The first inbox read after you upgrade only records the time, so older notifications never fire.

These three triggers are aimed at you, so they follow `inbox.auto_run` rather than a repo's `auto_run`, and they auto-run even without a rule. By default Claude reviews the PR or drafts a reply or fix, then asks before submitting the review, posting, or pushing. To let it act on its own, add a rule with an `allow` list:

```json
"pull_request:review_requested": { "allow": ["review"] },
"issue:mentioned": { "allow": ["comment"] },
"pull_request:mentioned": { "allow": ["comment"] }
```

A rule for one of these keys can also filter, for example `skip_bots` or `skip_labels`. To list them without running Claude, set `"inbox": { "auto_run": false }`. To stop reading notifications altogether, set `"enabled": false`.

### Safety defaults

- New repos are watched **without** auto-run. Turn it on per repo with `a`, and narrow it with `auto_rules` (see [Configuration](#configuration)). Events that already exist when you start watching a repo never auto-run, only ones that show up later. Other triggers auto-run only when an auto rule names them, except the personal ones from the [inbox](#inbox).
- Auto-run acts on one item at most `max_auto_runs` times (default 5), so a fix that keeps failing CI can't loop forever.
- Claude runs with `--permission-mode manual`, and only read-only tools are pre-approved (`Read`, `Grep`, `Glob`, `git status/diff/log/show`, and `gh pr view/diff/checks`, `gh issue view`, `gh run view/list`, and `gh api --method GET` for API reads). Anything else asks you first, including `gh api` writes.
- Claude's system prompt tells it to ask before pushing, opening PRs, commenting, labeling, submitting reviews, closing, merging or dismissing alerts, unless an auto rule's `allow` list pre-approves that action. It always asks before pushing to the default branch or force-pushing.
- Claude never works in your own checkouts. When you have a repo checked out locally (see [Local checkouts](#local-checkouts)), each session gets its own git worktree of it, so your working tree, current branch and uncommitted changes are never touched. Repos without a local checkout are cloned into the workspace, and sessions on the same clone run one at a time. At most `max_concurrent` sessions run overall.

### Local checkouts

Rather than cloning repos you already have, Fast Lane looks for them in `code_dirs` (by default `~/Code`, `~/code`, `~/src`, `~/Projects`, `~/Developer`, `~/dev` and `~/repos`, whichever exist). It searches up to three levels deep and reads each checkout's `.git/config` to see which GitHub repo its remotes point at. It understands SSH, HTTPS and `ssh://` URLs. If there are several matches, a remote named `origin` beats `upstream`, then a folder named after the repo wins, then the shorter path. The scan runs at startup and again on `R`, and repos with a checkout show `local` on the Repos tab. Setting a repo's `local_path` overrides the scan.

Each session runs in its own worktree under `<workspace_dir>/.worktrees/`. It's created from your checkout after a `git fetch`, detached at the remote's default branch. Worktrees share your checkout's objects and branches, so nothing is downloaded again, and anything Claude commits is a branch in your repo. Because worktrees are separate directories, sessions on the same repo can run in parallel. A finished session keeps its worktree for a week, so you can reply and pick up where it left off. After that the worktree is removed at startup, and replying recreates it. `D` on the Sessions tab deletes a session and its worktree right away. Uncommitted changes in a removed worktree are lost; committed branches stay.

## Configuration

Settings live in `~/.config/fast-lane/config.json` (or `$XDG_CONFIG_HOME`) and are written for you as you use the Repos, Rules and Settings tabs. Edits you make to the file by hand take effect when Fast Lane restarts. Use `-config <path>` to choose another file.

```json
{
  "poll_interval": "2m",
  "workspace_dir": "~/.local/share/fast-lane/workspaces",
  "code_dirs": ["~/Code", "~/src"],
  "max_concurrent": 2,
  "max_auto_runs": 5,
  "inbox": { "enabled": true, "auto_run": true },
  "claude": {
    "path": "claude",
    "model": "claude-opus-5-5",
    "effort": "medium",
    "permission_mode": "manual",
    "allowed_tools": ["Read", "Grep", "Glob", "Bash(gh pr view:*)"],
    "max_budget_usd": 5,
    "extra_args": []
  },
  "prompts": {
    "issue": "Triage issue #{{.Number}} in {{.Repo}}: {{.Title}}\n\n{{.Body}}"
  },
  "auto_rules": {
    "dependabot_alert": { "min_severity": "high", "model": "claude-haiku-5-5", "effort": "low", "allow": ["edit", "push_branch", "open_pr"] },
    "pull_request": { "skip_drafts": true, "skip_bots": true, "skip_authors": ["you"] },
    "pull_request:checks_failed": { "allow": ["edit", "update_pr"] },
    "pull_request:changes_requested": { "skip_bots": true, "allow": ["edit", "update_pr", "comment"] }
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

- **`inbox`**: read review requests, mentions and assignments from your notifications (`enabled`), and run Claude on them without asking (`auto_run`). Both default to on. See [Inbox](#inbox).
- **`max_auto_runs`**: the most times auto-run will start or resume a session for one item. Sessions you start yourself don't count.
- **`code_dirs`**: where to look for local checkouts (see [Local checkouts](#local-checkouts)). Set it to `[]` to always clone.
- **`local_path`** (per repo): the checkout to make worktrees from, when the scan doesn't find the right one.
- **`workspace_dir`**: repos without a local checkout are cloned here with `gh repo clone` on first use and fetched before each new session. Session worktrees live under `.worktrees/` here.
- **`model`** / **`effort`**: passed to `claude --model` and `claude --effort` (`low`, `medium`, `high`, `xhigh` or `max`). Leave either out to use Claude Code's default. A rule's `model` and `effort` replace these for its sessions.
- **`permission_mode`**: any mode `claude --permission-mode` accepts (`manual`, `acceptEdits`, `auto`, …). Prompts the mode doesn't settle on its own still come to the TUI.
- **`prompts`**: override the instructions for a kind (`pull_request`, `issue`, `dependabot_alert`, `code_scanning_alert`) or a kind and trigger (`pull_request:checks_failed`). An auto rule's `prompt` takes precedence. A trigger with no prompt of its own uses its kind's, with a line saying what triggered it. Templates use Go `text/template` and can use:
  - Every item: `.Repo`, `.Number`, `.Title`, `.URL`, `.Author`, `.Bot`, `.Labels`, `.Body`, `.Kind`, `.CreatedAt`, `.UpdatedAt`.
  - Alerts: `.Severity`.
  - PRs: `.Branch`, `.Draft`, `.HeadSHA`, `.Checks`, `.Mergeable`, `.ReviewDecision`.
  - The trigger: `.Trigger`, `.Actor` (who caused it) and `.Detail` (the comment or review text, the labels added, or the commit).

  The defaults are in [`internal/prompt/prompt.go`](internal/prompt/prompt.go).
- **`auto_rules`**: edit these in the Rules tab, or by hand here. The rule form shows only the fields that apply to the kind, checks the rule the same way loading the config does, and opens prompts in an editor that starts from the default template (`ctrl+r` resets to it). They choose what auto-runs on repos with `auto_run` on, keyed like `prompts`: a plain kind means `opened`, and `kind:trigger` covers another trigger. With no rules anywhere, every newly opened item auto-runs and no other trigger does. Once rules exist, only the keys listed auto-run, and only items that pass every filter set on the rule (an empty rule `{}` accepts every item). A key can also hold a list of rules, tried in order, where the first that accepts the item wins. For example, Dependabot PRs can get their own prompt and actions ahead of a general PR rule. A repo's own `auto_rules` replace the global ones entirely. Events that don't match still show up in the feed for you to start by hand. Filters, all compared without regard to case:
  - `min_severity` (alerts only): `low`, `medium`, `high` or `critical`. Code scanning's `note`, `warning` and `error` rank with `low`, `medium` and `high`.
  - `labels` / `skip_labels` (PRs and issues only): require at least one of these labels / skip events with any of them.
  - `authors` / `skip_authors`: require / skip these logins.
  - `skip_drafts` (PRs only): skip draft pull requests.
  - `skip_bots`: skip events opened by bot accounts, Dependabot alerts included.

  A rule also shapes the sessions it covers, whether they auto-run or you start them with `enter`:
  - `prompt`: a template that replaces the prompt for that kind and trigger (same fields as `prompts`).
  - `model`: the model those sessions run, such as `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5` or `claude-fable-5-1`, or an alias Claude Code accepts such as `opus`. It replaces the Model setting, so you can, say, give Dependabot bumps a fast model and review requests a stronger one. A session keeps its model across replies; when a trigger resumes it under another rule, it switches to that rule's model, or back to the default if that rule doesn't set one.
  - `effort`: the effort level those sessions run at (`low`, `medium`, `high`, `xhigh` or `max`), replacing the Effort setting the same way `model` does.
  - `allow`: actions Claude may take without asking. Each one pre-approves the tools below for that session and tells Claude it needs no approval. Anything not allowed still comes to you as a question or permission dialog.

  | Action | Pre-approves |
  |---|---|
  | `edit` | `Edit`, `Write`, `git checkout/switch/add/commit` |
  | `push_branch` | `git push` of new branches (never the default branch, never with force) |
  | `open_pr` | `gh pr create` |
  | `update_pr` | `gh pr checkout`, `git push`, `gh pr edit`, `gh pr ready` |
  | `comment` | `gh issue comment`, `gh pr comment`, and `gh api --method POST` on this PR's review comments, for replies in review threads |
  | `label` | `gh issue edit`, `gh pr edit` |
  | `review` | `gh pr review` |
  | `close` | `gh issue close`, `gh pr close` |
  | `merge` | `gh pr merge` |
  | `dismiss_alert` | `gh api --method PATCH` on this repo's Dependabot and code scanning alerts |

Runtime state lives in `~/.local/state/fast-lane/` (or `$XDG_STATE_HOME`): seen and dismissed events, each item's last snapshot and the changes that fired, inbox items from unwatched repos, session history in `state.json`, and the raw stream-json log of each session in `logs/`.

## Development

```sh
go test -race ./...
```

| Package | Role |
| --- | --- |
| `internal/github` | REST and GraphQL client built on `go-gh`, so it uses `gh`'s auth, plus change detection between polls and the notifications inbox |
| `internal/auto` | Matches items and triggers to auto rules and decides what auto-runs |
| `internal/claude` | Session manager: spawns `claude`, parses stream-json and answers `can_use_tool` control requests |
| `internal/tui` | Bubble Tea interface |
| `internal/local` | Finds local checkouts of GitHub repos in `code_dirs` |
| `internal/doctor` | Prerequisite checks shown on the Settings tab |
| `internal/config`, `internal/state`, `internal/prompt` | Settings, persistence and prompt templates |

## License

Fast Lane is licensed under the [PolyForm Shield License 1.0.0](LICENSE.md). You're free to use, modify and share it, including at work. What you can't do is use it to offer a product that competes with Fast Lane, such as selling it or a derivative of it as a product or service.

Because of that restriction, this is a *source-available* license rather than an OSI-approved open source license.
