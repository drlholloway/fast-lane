## What this changes

<!-- One or two sentences. Link the issue it addresses, if any: "Fixes #12". -->

## How it was tested

- [ ] `go test -race ./...` passes locally
- [ ] `go vet ./...` is clean and `gofmt -l .` prints nothing
- [ ] Tried in the app on: <!-- macOS / Linux, or "unit tests only" -->

## Checklist

- [ ] Changes to what Claude may do without asking (action tool patterns, default allowed tools, the system prompt) come with a test, and the PR says what they permit
- [ ] Tests still never touch GitHub, the real config, or real code folders
- [ ] Changed screens have regenerated wiki screenshots, or this PR says they need them
- [ ] The wiki page showing any changed key, setting, rule field or message is updated, or this PR says it needs to be
- [ ] `CHANGELOG.md` has a line under **Unreleased** if users would notice this
