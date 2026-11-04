package main

import (
	"fmt"
	"sort"
	"time"
)

// saleAlertCooldown limits "tickets available" pushes to one per event per window.
const saleAlertCooldown = 12 * time.Hour

type ChangeKind int

const (
	ChangeNew    ChangeKind = iota
	ChangeOnSale            // was sold out / not on sale, now buyable
	ChangeRescheduled
)

// Change is something worth a push notification. Apply mutates the state to
// record it; it is only called after the notification was delivered, so a
// failed push is retried on the next run instead of being lost.
type Change struct {
	Kind  ChangeKind
	UID   string
	Event Event
	Avail string
	Old   time.Time // previous start, for reschedules
	Apply func(*State)
}

// Diff compares the fetched feed against state. It returns the changes that
// need a notification, and directly applies silent bookkeeping (titles,
// URLs, gone flags, availability changes that aren't alert-worthy).
//
// avail may be nil when the availability endpoint failed; then availability
// is left untouched and no on-sale alerts are produced.
func Diff(s *State, feed []Event, avail map[string]string, now time.Time) []Change {
	var changes []Change
	inFeed := map[string]bool{}

	for _, ev := range feed {
		ev := ev
		inFeed[ev.UID] = true
		a := AvailUnknown
		if avail != nil {
			a = avail[urlPath(ev.URL)]
		}
		upcoming := isUpcoming(ev.Start, now)

		old, known := s.Events[ev.UID]
		if !known {
			newState := &EventState{Title: ev.Title, Start: ev.Start, URL: ev.URL, Avail: a, FirstSeen: now}
			if !upcoming {
				// Past events are recorded but never announced.
				s.Events[ev.UID] = newState
				continue
			}
			changes = append(changes, Change{
				Kind: ChangeNew, UID: ev.UID, Event: ev, Avail: a,
				Apply: func(st *State) { st.Events[ev.UID] = newState },
			})
			continue
		}

		old.Gone = false
		old.Title = ev.Title
		old.URL = ev.URL

		if upcoming && !old.Start.IsZero() && !ev.Start.IsZero() && !old.Start.Equal(ev.Start) {
			prev := old.Start
			changes = append(changes, Change{
				Kind: ChangeRescheduled, UID: ev.UID, Event: ev, Avail: a, Old: prev,
				Apply: func(st *State) { st.Events[ev.UID].Start = ev.Start },
			})
		} else if !old.Start.Equal(ev.Start) {
			old.Start = ev.Start // past event or missing date: update silently
		}

		if avail == nil || a == AvailUnknown {
			continue // keep last known availability
		}
		wasBlocked := old.Avail == AvailSoldOut || old.Avail == AvailNotOnSale
		cooledDown := now.Sub(old.SaleAlerted) > saleAlertCooldown
		if upcoming && wasBlocked && a == AvailOnSale && cooledDown {
			changes = append(changes, Change{
				Kind: ChangeOnSale, UID: ev.UID, Event: ev, Avail: a,
				Apply: func(st *State) {
					st.Events[ev.UID].Avail = a
					st.Events[ev.UID].SaleAlerted = now
				},
			})
		} else {
			old.Avail = a
		}
	}

	for uid, e := range s.Events {
		if !inFeed[uid] {
			e.Gone = true
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Event.Start.Before(changes[j].Event.Start)
	})
	return changes
}

func fmtWhen(t time.Time) string {
	if t.IsZero() {
		return "date TBA"
	}
	return t.In(berlin).Format("Mon 2 Jan, 15:04")
}

// Message renders a change into a notification.
func (c Change) Message() Notification {
	n := Notification{Click: c.Event.URL}
	status := availLabel(c.Avail)
	switch c.Kind {
	case ChangeNew:
		n.Title = "New ESN event: " + c.Event.Title
		n.Body = fmtWhen(c.Event.Start)
		if status != "" {
			n.Body += " · " + status
		}
		n.Priority = 4
		n.Tags = []string{"tada"}
	case ChangeOnSale:
		n.Title = "Tickets available: " + c.Event.Title
		n.Body = fmtWhen(c.Event.Start) + " · was sold out or not on sale before. Go now."
		n.Priority = 5
		n.Tags = []string{"rotating_light"}
	case ChangeRescheduled:
		n.Title = "Date changed: " + c.Event.Title
		n.Body = fmt.Sprintf("%s → %s", fmtWhen(c.Old), fmtWhen(c.Event.Start))
		n.Priority = 3
		n.Tags = []string{"calendar"}
	}
	return n
}
