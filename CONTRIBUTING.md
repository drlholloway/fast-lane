# Contributing to Fast Lane

Thanks for your interest. Fast Lane is a small project maintained by one
person, so the most useful contributions are focused ones: a clear bug report,
a rule that ran when it shouldn't have (or didn't when it should), a recipe
that worked well for you, or a fix with a test.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).
Security problems go through [private reporting](SECURITY.md), not issues.

## Ways to help

- **Report a bug.** Use the *Bug report* issue form. Say what you were doing,
  what Fast Lane showed, and which version (`fast-lane -version`).
- **Report a rule that misfired.** Use the *Rule didn't run as expected* form.
  The rule, the item and the trigger are usually enough to turn it into a test.
- **Suggest a feature.** Use the *Feature request* form.
- **Improve the wiki.** It is a normal GitHub wiki; edits are welcome,
  especially new [recipes](https://github.com/drlholloway/fast-lane/wiki/Recipes).

## Development setup

Requirements: Go 1.26.7 or newer and git. To run it you also need the GitHub
CLI, logged in, and Claude Code; the tests need neither.

```sh
git clone https://github.com/drlholloway/fast-lane.git
cd fast-lane
go build -o fast-lane .
go test -race ./...
go vet ./... && gofmt -l .          # gofmt -l must print nothing
```

The wiki's [Building from source](https://github.com/drlholloway/fast-lane/wiki/Building-from-Source)
page describes the packages. In short: `internal/tui` is the interface,
`internal/claude` runs sessions, `internal/github` talks to GitHub and detects
changes, `internal/auto` decides what runs, and `internal/config` holds the
settings, rules and actions.

## Pull requests

1. Open an issue first for anything beyond a small fix, so the approach can be
   agreed before you spend time on it.
2. Branch from `main`. Keep a PR to one change.
3. Add or update tests. The tests never touch GitHub, your config or your code
   folders; keep it that way, with the fakes in the existing tests.
4. Run `gofmt -l .`, `go vet ./...` and `go test -race ./...`. CI runs the same
   on Linux and macOS.
5. A change to what Claude may do without asking (an action's tool patterns,
   the default allowed tools, the system prompt) needs a test and a note in the
   PR about what it permits. Be conservative; Fast Lane runs unattended.
6. If you change a screen, regenerate the wiki screenshots (see Building from
   source) or say in the PR that they need it.
7. Update the wiki page that shows a changed key, setting, rule field or
   message, or say in the PR that it needs updating.
8. Add a line under **Unreleased** in `CHANGELOG.md` if users would notice
   the change.
9. Fill in the PR template.

## Releases

Maintainer only. Move the **Unreleased** section of `CHANGELOG.md` under a new
`## X.Y.Z — date` heading, commit, then tag that commit `vX.Y.Z` and push the
tag. The release workflow runs the tests, builds macOS and Linux archives with
the version baked in, and publishes a GitHub Release whose notes are that
changelog section.

## Style

American spelling in user-facing text and docs ("color", "behavior").
`gofmt` decides the rest.

## Licensing of contributions

Fast Lane is licensed under the [PolyForm Shield License 1.0.0](LICENSE.md).
By submitting a contribution you agree that it is licensed under the same
terms. Please do not submit code copied from projects under incompatible
licenses.

## Questions

Open an issue with the *question* label, or check the
[wiki](https://github.com/drlholloway/fast-lane/wiki) first.
