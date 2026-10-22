// Package state persists what Fast Lane has seen and done between runs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/drlholloway/fast-lane/internal/claude"
	"github.com/drlholloway/fast-lane/internal/github"
)

// State is saved to disk whenever something changes.
type State struct {
	// Seen records when each event key was first observed.
	Seen map[string]time.Time `json:"seen"`
	// Baselined marks repos whose first poll has completed. Events present
	// at that first poll are shown but never auto-run.
	Baselined map[string]bool `json:"baselined"`
	// Dismissed hides events from the feed until they change.
	Dismissed map[string]time.Time `json:"dismissed"`
	// EventSessions maps an event key to its most recent session ID.
	EventSessions map[string]int `json:"event_sessions"`
	// Snapshots hold each item's state at the last poll, to detect changes.
	Snapshots map[string]github.Snapshot `json:"snapshots"`
	// Fired lists the IDs of changes that already fired on each item.
	Fired map[string][]string `json:"fired"`
	// LastChange is the most recent change that fired on each item.
	LastChange map[string]github.Change `json:"last_change"`
	// Inbox holds review requests, mentions and assignments from repos the
	// user does not watch, by event key, so the feed keeps them across runs.
	Inbox map[string]github.Event `json:"inbox"`
	// Mentions records items that mention the user, by event key: MentionMe
	// for the user, MentionTeam for only a team the user is in.
	Mentions map[string]string `json:"mentions,omitempty"`
	// InboxSince is when the inbox was last read; zero until the first poll,
	// which only records the time so old notifications never fire.
	InboxSince time.Time `json:"inbox_since"`
	// AutoRuns counts sessions started or resumed without asking per item.
	AutoRuns map[string]int    `json:"auto_runs"`
	Sessions []*claude.Session `json:"sessions"`

	path string
}

// Load reads state from path, returning empty state when it does not exist.
func Load(path string) (*State, error) {
	st := &State{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	st.init()
	return st, nil
}

func (s *State) init() {
	if s.Seen == nil {
		s.Seen = map[string]time.Time{}
	}
	if s.Baselined == nil {
		s.Baselined = map[string]bool{}
	}
	if s.Dismissed == nil {
		s.Dismissed = map[string]time.Time{}
	}
	if s.EventSessions == nil {
		s.EventSessions = map[string]int{}
	}
	if s.Snapshots == nil {
		s.Snapshots = map[string]github.Snapshot{}
	}
	if s.Fired == nil {
		s.Fired = map[string][]string{}
	}
	if s.LastChange == nil {
		s.LastChange = map[string]github.Change{}
	}
	if s.AutoRuns == nil {
		s.AutoRuns = map[string]int{}
	}
	if s.Inbox == nil {
		s.Inbox = map[string]github.Event{}
	}
	if s.Mentions == nil {
		s.Mentions = map[string]string{}
	}
}

// Kinds of mention recorded in Mentions.
const (
	MentionMe   = "me"
	MentionTeam = "team"
)

// NoteMention records that key mentions the user, or only a team the user
// is in. A direct mention is never downgraded to a team one.
func (s *State) NoteMention(key string, team bool) {
	if team && s.Mentions[key] == "" {
		s.Mentions[key] = MentionTeam
	} else if !team {
		s.Mentions[key] = MentionMe
	}
}

// Save writes the state atomically.
func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Observe records an event key and reports whether it is new, meaning it
// was not seen before and the repo already had its baseline poll.
func (s *State) Observe(repo, key string, now time.Time) bool {
	if _, ok := s.Seen[key]; ok {
		return false
	}
	s.Seen[key] = now
	return s.Baselined[strings.ToLower(repo)]
}

// MarkBaselined records that repo has completed its first poll.
func (s *State) MarkBaselined(repo string) { s.Baselined[strings.ToLower(repo)] = true }

// maxFired bounds how many change IDs are remembered per item.
const maxFired = 50

// Fire records change c on key and reports whether it is the first time.
func (s *State) Fire(key string, c github.Change) bool {
	if slices.Contains(s.Fired[key], c.ID) {
		return false
	}
	f := append(s.Fired[key], c.ID)
	if len(f) > maxFired {
		f = f[len(f)-maxFired:]
	}
	s.Fired[key] = f
	s.LastChange[key] = c
	return true
}

// PruneInbox forgets inbox items with no activity since cutoff.
func (s *State) PruneInbox(cutoff time.Time) {
	for k, ev := range s.Inbox {
		if ev.UpdatedAt.Before(cutoff) {
			delete(s.Inbox, k)
			delete(s.Mentions, k)
		}
	}
}

// IsDismissed reports whether key was dismissed after its last update.
func (s *State) IsDismissed(key string, updated time.Time) bool {
	t, ok := s.Dismissed[key]
	return ok && !updated.After(t)
}

// Prune forgets seen events older than maxAge that are no longer open.
func (s *State) Prune(open map[string]bool, maxAge time.Duration, now time.Time) {
	for k, t := range s.Seen {
		if !open[k] && now.Sub(t) > maxAge {
			delete(s.Seen, k)
			delete(s.Dismissed, k)
			delete(s.Snapshots, k)
			delete(s.Fired, k)
			delete(s.LastChange, k)
			delete(s.AutoRuns, k)
			delete(s.Mentions, k)
		}
	}
}
