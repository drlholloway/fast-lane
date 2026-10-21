package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drlholloway/fast-lane/internal/claude"
)

func TestObserveBaseline(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if st.Observe("O/R", "o/r:issue:1", now) {
		t.Fatal("events before the baseline are not new")
	}
	st.MarkBaselined("O/R")
	if st.Observe("o/r", "o/r:issue:1", now) {
		t.Fatal("already seen")
	}
	if !st.Observe("o/r", "o/r:issue:2", now) {
		t.Fatal("unseen event after baseline is new")
	}
}

func TestSaveLoadAndDismiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, _ := Load(path)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.Dismissed["k"] = t0
	st.Sessions = []*claude.Session{{ID: 3, Status: claude.StatusIdle, ClaudeID: "abc"}}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsDismissed("k", t0) || got.IsDismissed("k", t0.Add(time.Hour)) {
		t.Fatal("dismissal should hold until the event updates")
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ClaudeID != "abc" {
		t.Fatalf("sessions not restored: %+v", got.Sessions)
	}

	got.Seen["old"] = t0
	got.Seen["open"] = t0
	got.Prune(map[string]bool{"open": true}, 24*time.Hour, t0.Add(48*time.Hour))
	if _, ok := got.Seen["old"]; ok {
		t.Fatal("old closed event should be pruned")
	}
	if _, ok := got.Seen["open"]; !ok {
		t.Fatal("open event must be kept")
	}
}
