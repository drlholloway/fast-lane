# Security Policy

## Supported versions

Only the latest release on the
[Releases page](https://github.com/drlholloway/fast-lane/releases) receives
fixes. Upgrade before reporting; `fast-lane -version` says what you have.

## What counts

Fast Lane runs on your machine with your GitHub CLI login, and starts Claude
Code sessions that can change code and act on GitHub as you. It keeps no
credentials of its own, makes no network calls except to GitHub through the
GitHub CLI's login and through `gh`, `git` and `claude`, and has no telemetry.
The interesting surface is:

- **Other people's text reaching Claude.** Pull request descriptions, issue
  bodies, comments and reviews written by anyone go into Claude's
  instructions. Anything that lets that text get Claude to do something it
  wasn't allowed to do, without the permission dialog appearing, is in scope:
  for example a way around the pre-approved tools, or an action that runs
  under a rule's `allow` list on an item the rule shouldn't have matched.
- **Permissions Fast Lane grants.** Each allowed action pre-approves specific
  tool patterns, listed on the wiki's
  [Auto rules](https://github.com/drlholloway/fast-lane/wiki/Auto-Rules#actions)
  page. A pattern that allows more than it says, such as `gh api` writes
  getting through the read-only default or a reply rule reaching another
  repository, is in scope.
- **Your checkouts and files.** Fast Lane should only run `git fetch` and
  `git worktree add` in your own checkouts, and only write under its
  workspace, its config and its state folders. Anything that makes it change
  your working tree, branch or files elsewhere is in scope.
- **Your GitHub login.** Anything that exposes the token or uses it for
  something you didn't set up, such as a write you didn't allow, is in scope.
- **Dependencies** pulled in through Go modules and GitHub Actions.

Some limits are held by Claude's instructions rather than by permissions, and
the wiki's [Safety](https://github.com/drlholloway/fast-lane/wiki/Safety) page
says which: never pushing to the default branch or force-pushing when pushing
is allowed, and `label` changing only labels. Claude ignoring one of those is
worth a regular issue, but it is a known limit, not a vulnerability. So is
anything Claude does within actions you allowed. Vulnerabilities in Claude Code
itself go to [Anthropic](https://www.anthropic.com/responsible-disclosure-policy),
and in the GitHub CLI to [GitHub](https://github.com/cli/cli/security).

## Reporting a vulnerability

Please do not open a public issue for a security problem. Use GitHub's private
reporting instead: **Security → Report a vulnerability** on the repository, or
https://github.com/drlholloway/fast-lane/security/advisories/new

Include your version, platform, what you observed and how to reproduce it.
The session log (`~/.local/state/fast-lane/logs/session-<n>.jsonl`) and the
relevant part of your config help a lot; remove anything private first. You
will get an acknowledgement within a week. Fixes ship as a new release with a
credit in the changelog unless you prefer to stay anonymous.

## Dependencies

Dependabot watches the Go module and GitHub Actions dependencies weekly, and
CodeQL scans the code on every push.
