# Changelog

All notable changes to Fast Lane. The section for a tagged version becomes the
GitHub Release notes.

## 0.2.1 — 2026-10-22

### Added
- Fast Lane looks for a newer release when it starts and once a day. When
  one is out, a line above the key help says so, and Settings → Version
  shows it; `enter` there opens the release page. `c` on the Settings tab
  looks again.

## 0.2.0 — 2026-10-21

### Added
- **Full auto**, a third auto-run setting per repo and for the inbox. Every
  trigger runs and Claude acts on its own judgment: approving or requesting
  changes, merging, pushing, commenting and closing, without asking. Only a
  human may delete or transfer a repository or destroy its history: Fast Lane
  checks every command a full-auto session runs and refuses those, adds
  GitHub rulesets protecting the default branch and tags when you turn it on,
  and warns if your login has the `delete_repo` scope.
- A security policy, contributing guide, code of conduct, issue and pull
  request templates, CI on Linux and macOS, and release builds for macOS and
  Linux attached to each GitHub Release.

## 0.1.1 — 2026-10-21

### Fixed
- Resizing the terminal while the rule form was open (outside its prompt
  editor), or while a single-line setting such as Poll interval was being
  edited, crashed Fast Lane.

### Added
- Screenshots of every tab and dialog on the wiki, and a link to the wiki in
  the README. `FASTLANE_SCREENSHOTS=<folder> go test ./internal/tui -run
  TestScreenshots` renders them from made-up demo data.

## 0.1.0 — 2026-10-21

First release.

### Added
- **Watching repos**: pull requests, issues, Dependabot alerts and code
  scanning alerts on the repos you pick, through the GitHub CLI's login.
- **Triggers**: opened, changes requested, checks failed, conflict,
  commented, review requested, mentioned, assigned, ready for review, pushed
  and labeled. Your own activity and bot comments never trigger anything, each
  change fires once, and `max_auto_runs` caps runs per item.
- **Inbox**: review requests, @-mentions and assignments on any repo, from
  your notifications. Claude prepares the review or reply and asks before
  posting, unless a rule allows it.
- **Auto rules** keyed by kind and trigger, with filters for severity,
  labels, authors, drafts and bots; several rules per key, first match wins;
  global and per-repo rules. Each sets the session's prompt, model, effort
  and pre-approved actions: edit, push_branch, open_pr, update_pr, comment,
  label, review, close, merge and dismiss_alert.
- **Sessions** run `claude` in streaming mode, show permission requests and
  questions as dialogs, and resume when a trigger fires on the same item.
- **Local checkouts**: found in `code_dirs`, used through a git worktree per
  session, so your working tree is never touched. Repos without one are
  cloned into the workspace.
- **Rules tab** to edit auto rules, and **Settings tab** with prerequisite
  checks and every setting, including Claude Code's model and effort.
