// Package state persists what Fast Lane has seen and done between runs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drlholloway/fast-lane/internal/claude"
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
	EventSessions map[string]int    `json:"event_sessions"`
	Sessions      []*claude.Session `json:"sessions"`

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
		}
	}
}
