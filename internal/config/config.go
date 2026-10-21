// Package config loads and saves Fast Lane's user configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"
)

// EventKind identifies a type of GitHub activity Fast Lane can watch.
type EventKind string

const (
	KindPullRequest  EventKind = "pull_request"
	KindIssue        EventKind = "issue"
	KindDependabot   EventKind = "dependabot_alert"
	KindCodeScanning EventKind = "code_scanning_alert"
)

// AllKinds lists every supported event kind in display order.
var AllKinds = []EventKind{KindPullRequest, KindIssue, KindDependabot, KindCodeScanning}

// Short returns a compact label for the kind.
func (k EventKind) Short() string {
	switch k {
	case KindPullRequest:
		return "PR"
	case KindIssue:
		return "Issue"
	case KindDependabot:
		return "Dependabot"
	case KindCodeScanning:
		return "CodeScan"
	}
	return string(k)
}

// Trigger names a change to an item that can start or resume a session.
type Trigger string

const (
	TriggerOpened           Trigger = "opened"
	TriggerPushed           Trigger = "pushed"
	TriggerChecksFailed     Trigger = "checks_failed"
	TriggerConflict         Trigger = "conflict"
	TriggerChangesRequested Trigger = "changes_requested"
	TriggerCommented        Trigger = "commented"
	TriggerReviewRequested  Trigger = "review_requested"
	TriggerReadyForReview   Trigger = "ready_for_review"
	TriggerLabeled          Trigger = "labeled"
	TriggerMentioned        Trigger = "mentioned"
	TriggerAssigned         Trigger = "assigned"
)

// Personal reports whether t is aimed at the user (a review request, a
// mention, an assignment). Personal triggers can come from repos the user
// does not watch, follow Inbox.AutoRun instead of the repo's auto_run, and
// auto-run with an empty rule unless auto_rules say otherwise.
func (t Trigger) Personal() bool {
	return t == TriggerReviewRequested || t == TriggerMentioned || t == TriggerAssigned
}

// Triggers lists the triggers k supports, most important first. When one
// poll finds several changes on an item, the first that has a matching rule
// is the one acted on.
func (k EventKind) Triggers() []Trigger {
	switch k {
	case KindPullRequest:
		return []Trigger{TriggerOpened, TriggerChangesRequested, TriggerChecksFailed, TriggerConflict,
			TriggerCommented, TriggerReviewRequested, TriggerMentioned, TriggerAssigned, TriggerReadyForReview,
			TriggerPushed, TriggerLabeled}
	case KindIssue:
		return []Trigger{TriggerOpened, TriggerCommented, TriggerMentioned, TriggerAssigned, TriggerLabeled}
	}
	return []Trigger{TriggerOpened}
}

// Short returns a compact label for the trigger.
func (t Trigger) Short() string {
	switch t {
	case TriggerChecksFailed:
		return "CI failed"
	case TriggerChangesRequested:
		return "changes requested"
	case TriggerReviewRequested:
		return "review requested"
	case TriggerReadyForReview:
		return "ready for review"
	}
	return string(t)
}

// RuleKey keys auto rules and prompts: "kind" for the opened trigger or
// "kind:trigger" for any other.
type RuleKey string

// Key returns the canonical RuleKey for kind k and trigger t.
func Key(k EventKind, t Trigger) RuleKey {
	if t == TriggerOpened || t == "" {
		return RuleKey(k)
	}
	return RuleKey(string(k) + ":" + string(t))
}

// Parse splits the key, checking that the kind supports the trigger.
func (rk RuleKey) Parse() (EventKind, Trigger, error) {
	k, t, _ := strings.Cut(string(rk), ":")
	kind := EventKind(k)
	if !slices.Contains(AllKinds, kind) {
		return "", "", fmt.Errorf("unknown event kind %q", k)
	}
	trig := TriggerOpened
	if t != "" {
		trig = Trigger(t)
	}
	if !slices.Contains(kind.Triggers(), trig) {
		return "", "", fmt.Errorf("%s has no trigger %q", kind, t)
	}
	return kind, trig, nil
}

// canonical rewrites keys such as "issue:opened" to "issue", validating each.
func canonical[V any](m map[RuleKey]V, check func(RuleKey, EventKind, V) error) (map[RuleKey]V, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[RuleKey]V, len(m))
	for rk, v := range m {
		k, t, err := rk.Parse()
		if err != nil {
			return nil, err
		}
		key := Key(k, t)
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%q and %q are the same key", rk, key)
		}
		if err := check(key, k, v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

func checkRules(key RuleKey, k EventKind, rs Rules) error {
	for _, r := range rs {
		if err := r.validate(key, k); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRule checks rule r under key the way Load does.
func ValidateRule(key RuleKey, r Rule) error {
	k, t, err := key.Parse()
	if err != nil {
		return err
	}
	return r.validate(Key(k, t), k)
}

func checkPrompt(key RuleKey, _ EventKind, src string) error {
	if err := ValidatePrompt(src); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// ValidatePrompt checks that src parses as a prompt template.
func ValidatePrompt(src string) error {
	_, err := template.New("prompt").Parse(src)
	return err
}

// Duration is a time.Duration that marshals as a human-readable string ("2m").
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Repo holds the per-repository watch settings.
type Repo struct {
	// FullName is "owner/name".
	FullName string `json:"full_name"`
	// LocalPath is an existing checkout to work from, overriding the one
	// found in CodeDirs. Sessions use git worktrees of it, never the
	// checkout itself.
	LocalPath string `json:"local_path,omitempty"`
	// Events lists which kinds of activity to watch.
	Events []EventKind `json:"events"`
	// AutoRun starts a Claude session for new events without asking.
	AutoRun bool `json:"auto_run"`
	// AutoRules, when set, replace Config.AutoRules for this repo.
	AutoRules map[RuleKey]Rules `json:"auto_rules,omitempty"`
}

// Rule filters which new events of one kind auto-run. Every field that is set
// must match; list fields match when any entry does. Comparisons ignore case.
type Rule struct {
	// MinSeverity skips alerts below this level. See SeverityRank.
	MinSeverity string `json:"min_severity,omitempty"`
	// Labels requires at least one of these labels.
	Labels []string `json:"labels,omitempty"`
	// SkipLabels skips events carrying any of these labels.
	SkipLabels []string `json:"skip_labels,omitempty"`
	// Authors requires the event to be opened by one of these logins.
	Authors []string `json:"authors,omitempty"`
	// SkipAuthors skips events opened by any of these logins.
	SkipAuthors []string `json:"skip_authors,omitempty"`
	// SkipDrafts skips draft pull requests.
	SkipDrafts bool `json:"skip_drafts,omitempty"`
	// SkipBots skips events opened by bot accounts.
	SkipBots bool `json:"skip_bots,omitempty"`

	// Prompt replaces the kind's prompt template for events this rule accepts.
	Prompt string `json:"prompt,omitempty"`
	// Allow lists actions Claude may take without asking.
	Allow []Action `json:"allow,omitempty"`
	// Model replaces Claude.Model for sessions this rule governs, such as
	// claude-opus-5-5 or an alias like sonnet.
	Model string `json:"model,omitempty"`
	// Effort replaces Claude.Effort for sessions this rule governs.
	Effort string `json:"effort,omitempty"`
}

// Rules are the rules for one key, tried in order; the first that accepts an
// item governs it. In JSON a single rule may be written as an object.
type Rules []Rule

func (rs Rules) MarshalJSON() ([]byte, error) {
	var v any = []Rule(rs)
	if len(rs) == 1 {
		v = rs[0]
	}
	// Encode without HTML escaping, which json.Marshal would apply to the
	// prompts no matter how the outer encoder is set.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func (rs *Rules) UnmarshalJSON(b []byte) error {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		var r Rule
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		*rs = Rules{r}
		return nil
	}
	return json.Unmarshal(b, (*[]Rule)(rs))
}

// Action is something Claude can be pre-approved to do in a session.
type Action string

const (
	ActionEdit         Action = "edit"
	ActionPushBranch   Action = "push_branch"
	ActionOpenPR       Action = "open_pr"
	ActionUpdatePR     Action = "update_pr"
	ActionComment      Action = "comment"
	ActionLabel        Action = "label"
	ActionReview       Action = "review"
	ActionClose        Action = "close"
	ActionMerge        Action = "merge"
	ActionDismissAlert Action = "dismiss_alert"
)

// AllActions lists every action in the order they are described to Claude.
var AllActions = []Action{
	ActionEdit, ActionPushBranch, ActionOpenPR, ActionUpdatePR, ActionComment, ActionLabel,
	ActionReview, ActionClose, ActionMerge, ActionDismissAlert,
}

// actionInfo describes each action. "{repo}" and "{number}" in a tool rule
// are replaced with the session's repository and item number.
var actionInfo = map[Action]struct {
	desc  string
	tools []string
}{
	ActionEdit: {"", []string{"Edit", "Write",
		"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git add:*)", "Bash(git commit:*)"}},
	ActionPushBranch: {"push new branches", []string{"Bash(git push:*)"}},
	ActionOpenPR:     {"open pull requests", []string{"Bash(gh pr create:*)"}},
	ActionUpdatePR: {"update existing pull requests (push commits to their branch, edit their title and description, mark them ready for review)",
		[]string{"Bash(gh pr checkout:*)", "Bash(git push:*)", "Bash(gh pr edit:*)", "Bash(gh pr ready:*)"}},
	ActionComment: {"comment on issues and pull requests, including replies in review threads", []string{
		"Bash(gh issue comment:*)", "Bash(gh pr comment:*)",
		"Bash(gh api --method POST repos/{repo}/pulls/{number}/comments:*)"}},
	ActionLabel:  {"add or remove labels", []string{"Bash(gh issue edit:*)", "Bash(gh pr edit:*)"}},
	ActionReview: {"submit pull request reviews", []string{"Bash(gh pr review:*)"}},
	ActionClose:  {"close issues or pull requests", []string{"Bash(gh issue close:*)", "Bash(gh pr close:*)"}},
	ActionMerge:  {"merge pull requests", []string{"Bash(gh pr merge:*)"}},
	ActionDismissAlert: {"dismiss security alerts", []string{
		"Bash(gh api --method PATCH repos/{repo}/dependabot/alerts/:*)",
		"Bash(gh api --method PATCH repos/{repo}/code-scanning/alerts/:*)"}},
}

// Describe returns how the action is named in Claude's instructions. It is
// empty for actions that only pre-approve tools.
func (a Action) Describe() string { return actionInfo[a].desc }

// Tools returns the permission rules the action pre-approves for item number
// in repo.
func (a Action) Tools(repo string, number int) []string {
	r := strings.NewReplacer("{repo}", repo, "{number}", strconv.Itoa(number))
	out := make([]string, len(actionInfo[a].tools))
	for i, t := range actionInfo[a].tools {
		out[i] = r.Replace(t)
	}
	return out
}

func (r Rule) validate(key RuleKey, kind EventKind) error {
	k := key
	alert := kind == KindDependabot || kind == KindCodeScanning
	switch {
	case r.MinSeverity != "" && !alert:
		return fmt.Errorf("%s: min_severity only applies to alerts", k)
	case r.MinSeverity != "" && SeverityRank(r.MinSeverity) == 0:
		return fmt.Errorf("%s: unknown min_severity %q", k, r.MinSeverity)
	case (len(r.Labels) > 0 || len(r.SkipLabels) > 0) && alert:
		return fmt.Errorf("%s: labels and skip_labels only apply to pull requests and issues", k)
	case r.SkipDrafts && kind != KindPullRequest:
		return fmt.Errorf("%s: skip_drafts only applies to pull requests", k)
	}
	if strings.ContainsFunc(r.Model, unicode.IsSpace) {
		return fmt.Errorf("%s: model %q cannot contain spaces", k, r.Model)
	}
	if r.Effort != "" && !slices.Contains(Efforts, r.Effort) {
		return fmt.Errorf("%s: effort %q must be one of %s", k, r.Effort, strings.Join(Efforts, ", "))
	}
	for _, a := range r.Allow {
		if _, ok := actionInfo[a]; !ok {
			return fmt.Errorf("%s: unknown action %q in allow", k, a)
		}
	}
	if _, err := template.New(string(k)).Parse(r.Prompt); err != nil {
		return fmt.Errorf("%s: prompt: %w", k, err)
	}
	return nil
}

// SeverityRank orders alert severities from 1 (lowest) to 4, returning 0 for
// unknown values. Code scanning's note, warning and error rank with low,
// medium and high.
func SeverityRank(s string) int {
	switch strings.ToLower(s) {
	case "low", "note":
		return 1
	case "medium", "moderate", "warning":
		return 2
	case "high", "error":
		return 3
	case "critical":
		return 4
	}
	return 0
}

// Watches reports whether the repo watches the given kind.
func (r Repo) Watches(k EventKind) bool {
	for _, e := range r.Events {
		if e == k {
			return true
		}
	}
	return false
}

// Claude holds settings for launching Claude Code sessions.
type Claude struct {
	// Path is the claude executable.
	Path string `json:"path"`
	// Model is passed to --model when set.
	Model string `json:"model,omitempty"`
	// Effort is passed to --effort when set: one of Efforts.
	Effort string `json:"effort,omitempty"`
	// PermissionMode is passed to --permission-mode. Prompts that the mode
	// does not settle on its own are shown in the TUI.
	PermissionMode string `json:"permission_mode"`
	// AllowedTools are pre-approved and never prompt.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// MaxBudgetUSD caps the spend of a single session when greater than zero.
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`
	// ExtraArgs are appended to every claude invocation.
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// Efforts lists the effort levels claude --effort accepts, lowest first.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Config is the full user configuration.
type Config struct {
	PollInterval  Duration `json:"poll_interval"`
	WorkspaceDir  string   `json:"workspace_dir"`
	MaxConcurrent int      `json:"max_concurrent"`
	Claude        Claude   `json:"claude"`
	Inbox         Inbox    `json:"inbox"`
	// CodeDirs are searched for existing checkouts of repositories, which
	// sessions use through git worktrees instead of cloning.
	CodeDirs []string `json:"code_dirs"`
	// MaxAutoRuns caps how many times triggers may auto-run Claude on one
	// item, so a fix that keeps failing CI cannot loop forever.
	MaxAutoRuns int `json:"max_auto_runs"`
	// Prompts overrides the default prompt template for a kind or trigger.
	// Templates use Go text/template syntax; see the README for fields.
	Prompts map[RuleKey]string `json:"prompts,omitempty"`
	// AutoRules limits auto-run to the kinds and triggers listed, filtered
	// by their rule. When no rules are set anywhere, every newly opened item
	// auto-runs and other triggers never do.
	AutoRules map[RuleKey]Rules `json:"auto_rules,omitempty"`
	Repos     []Repo            `json:"repos"`

	path string
}

// Inbox controls watching the user's GitHub notifications for review
// requests, mentions and assignments in any repository.
type Inbox struct {
	Enabled bool `json:"enabled"`
	// AutoRun starts or resumes sessions for personal triggers without
	// asking. Claude still asks before posting anything unless a rule's
	// allow list says otherwise.
	AutoRun bool `json:"auto_run"`
}

// Default returns a configuration with sensible defaults.
func Default() *Config {
	return &Config{
		PollInterval:  Duration{2 * time.Minute},
		WorkspaceDir:  filepath.Join(dataHome(), "fast-lane", "workspaces"),
		MaxConcurrent: 2,
		MaxAutoRuns:   5,
		Inbox:         Inbox{Enabled: true, AutoRun: true},
		CodeDirs:      []string{"~/Code", "~/code", "~/src", "~/Projects", "~/Developer", "~/dev", "~/repos"},
		Claude: Claude{
			Path:           "claude",
			PermissionMode: "manual",
			AllowedTools: []string{
				"Read", "Grep", "Glob",
				"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
				"Bash(gh pr view:*)", "Bash(gh pr diff:*)", "Bash(gh pr checks:*)",
				"Bash(gh issue view:*)", "Bash(gh run view:*)", "Bash(gh run list:*)",
				"Bash(gh api --method GET:*)", "Bash(gh api -X GET:*)",
			},
		},
	}
}

// DefaultPath returns the default location of the config file.
func DefaultPath() string {
	return filepath.Join(configHome(), "fast-lane", "config.json")
}

// Load reads the config at path, returning defaults when it does not exist.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.WorkspaceDir = ExpandHome(cfg.WorkspaceDir)
	if cfg.Prompts, err = canonical(cfg.Prompts, checkPrompt); err != nil {
		return nil, fmt.Errorf("%s: prompts: %w", path, err)
	}
	if cfg.AutoRules, err = canonical(cfg.AutoRules, checkRules); err != nil {
		return nil, fmt.Errorf("%s: auto_rules: %w", path, err)
	}
	for i := range cfg.Repos {
		r := &cfg.Repos[i]
		r.LocalPath = ExpandHome(r.LocalPath)
		if r.AutoRules, err = canonical(r.AutoRules, checkRules); err != nil {
			return nil, fmt.Errorf("%s: %s auto_rules: %w", path, r.FullName, err)
		}
	}
	if cfg.MaxAutoRuns < 1 {
		cfg.MaxAutoRuns = 1
	}
	if e := cfg.Claude.Effort; e != "" && !slices.Contains(Efforts, e) {
		return nil, fmt.Errorf("%s: claude.effort %q must be one of %s", path, e, strings.Join(Efforts, ", "))
	}
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	if cfg.PollInterval.Duration < 30*time.Second {
		cfg.PollInterval.Duration = 30 * time.Second
	}
	return cfg, nil
}

// Path returns the file the config was loaded from.
func (c *Config) Path() string { return c.path }

// Save writes the config back to the file it was loaded from.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config has no path")
	}
	sort.Slice(c.Repos, func(i, j int) bool { return c.Repos[i].FullName < c.Repos[j].FullName })
	// Prompts are edited by hand, so keep <, > and & readable.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return err
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Repo returns the watch settings for fullName, or nil when it is not watched.
func (c *Config) Repo(fullName string) *Repo {
	for i := range c.Repos {
		if strings.EqualFold(c.Repos[i].FullName, fullName) {
			return &c.Repos[i]
		}
	}
	return nil
}

// Watch starts watching fullName with all event kinds enabled. It is a no-op
// when the repo is already watched.
func (c *Config) Watch(fullName string) *Repo {
	if r := c.Repo(fullName); r != nil {
		return r
	}
	c.Repos = append(c.Repos, Repo{FullName: fullName, Events: append([]EventKind(nil), AllKinds...)})
	return &c.Repos[len(c.Repos)-1]
}

// Unwatch stops watching fullName.
func (c *Config) Unwatch(fullName string) {
	for i := range c.Repos {
		if strings.EqualFold(c.Repos[i].FullName, fullName) {
			c.Repos = append(c.Repos[:i], c.Repos[i+1:]...)
			return
		}
	}
}

// RulesFor returns the auto rules in effect for r: its own when it has any,
// otherwise the global ones. Nil means every kind auto-runs unfiltered.
func (c *Config) RulesFor(r Repo) map[RuleKey]Rules {
	if len(r.AutoRules) > 0 {
		return r.AutoRules
	}
	if len(c.AutoRules) > 0 {
		return c.AutoRules
	}
	return nil
}

// Clone returns a deep copy of c that shares nothing it could mutate.
func (c *Config) Clone() *Config {
	out := *c
	out.Claude.AllowedTools = slices.Clone(c.Claude.AllowedTools)
	out.Claude.ExtraArgs = slices.Clone(c.Claude.ExtraArgs)
	out.CodeDirs = slices.Clone(c.CodeDirs)
	out.Prompts = maps.Clone(c.Prompts)
	out.AutoRules = cloneRules(c.AutoRules)
	out.Repos = make([]Repo, len(c.Repos))
	for i, r := range c.Repos {
		r.Events = slices.Clone(r.Events)
		r.AutoRules = cloneRules(r.AutoRules)
		out.Repos[i] = r
	}
	return &out
}

func cloneRules(m map[RuleKey]Rules) map[RuleKey]Rules {
	if m == nil {
		return nil
	}
	out := make(map[RuleKey]Rules, len(m))
	for k, rs := range m {
		out[k] = slices.Clone(rs)
	}
	return out
}

// SearchDirs returns CodeDirs with ~ expanded.
func (c *Config) SearchDirs() []string {
	out := make([]string, len(c.CodeDirs))
	for i, d := range c.CodeDirs {
		out[i] = ExpandHome(d)
	}
	return out
}

// CloneDir is where fullName is cloned when there is no local checkout.
func (c *Config) CloneDir(fullName string) string {
	return filepath.Join(c.WorkspaceDir, filepath.FromSlash(fullName))
}

// WorktreeDir is where session id's worktree of fullName goes.
func (c *Config) WorktreeDir(fullName string, id int) string {
	return filepath.Join(c.WorkspaceDir, ".worktrees", filepath.FromSlash(fullName), fmt.Sprintf("session-%d", id))
}

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home(), ".config")
}

func dataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(home(), ".local", "share")
}

// StateHome returns the base directory for runtime state.
func StateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "fast-lane")
	}
	return filepath.Join(home(), ".local", "state", "fast-lane")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}
