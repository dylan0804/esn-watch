package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"
)

const stateVersion = 1

type EventState struct {
	Title     string    `json:"title"`
	Start     time.Time `json:"start"`
	URL       string    `json:"url"`
	Avail     string    `json:"avail,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	// SaleAlerted is when we last sent "tickets available". pretix flips an event
	// between sold out and available as unpaid carts expire; this stops a
	// priority-5 alert every 15 minutes for one popular trip.
	SaleAlerted time.Time `json:"sale_alerted,omitempty"`
	// Gone is set when the event disappears from the feed. We keep it so a
	// feed hiccup or a delete-and-re-add doesn't trigger a second "new" alert.
	Gone bool `json:"gone,omitempty"`
}

type State struct {
	Version     int                    `json:"version"`
	Initialized bool                   `json:"initialized"`
	Events      map[string]*EventState `json:"events"`
	FailStreak  int                    `json:"fail_streak"`
	// SuspiciousStreak counts consecutive runs whose feed parsed fine but looked
	// wrong (empty / mostly vanished). Network errors don't reset or advance it.
	SuspiciousStreak int    `json:"suspicious_streak,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	BrokenAlert      bool   `json:"broken_alert_sent,omitempty"`
	// Touched is bumped at least every keepAliveEvery so the repo gets a commit
	// and GitHub doesn't disable the schedule after 60 days of inactivity.
	Touched time.Time `json:"touched"`
}

func NewState() *State {
	return &State{Version: stateVersion, Events: map[string]*EventState{}}
}

// LoadState returns (state, existed, error).
func LoadState(path string) (*State, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s := NewState()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, true, err
	}
	if s.Events == nil {
		s.Events = map[string]*EventState{}
	}
	return s, true, nil
}

// Save writes atomically (temp file + rename) with stable, readable formatting.
// encoding/json sorts map keys, so diffs in git stay small.
func (s *State) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ActiveUpcoming counts events that are in the feed and haven't started yet.
func (s *State) ActiveUpcoming(now time.Time) int {
	n := 0
	for _, e := range s.Events {
		if !e.Gone && isUpcoming(e.Start, now) {
			n++
		}
	}
	return n
}

// Prune drops events that have left the feed and started more than keepFor ago.
// Events still in the feed are never pruned, and past events are never alerted
// on, so a pruned event can't come back as a false "new" alert.
func (s *State) Prune(now time.Time, keepFor time.Duration) {
	for uid, e := range s.Events {
		if e.Gone && !e.Start.IsZero() && e.Start.Before(now.Add(-keepFor)) {
			delete(s.Events, uid)
		}
	}
}

// isUpcoming treats an unknown (zero) start as upcoming: better one alert with
// "date TBA" than silently filing a new event as past.
func isUpcoming(start, now time.Time) bool {
	return start.IsZero() || start.After(now)
}
