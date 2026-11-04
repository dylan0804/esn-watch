package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shaped like the real pretix organizer feed: Berlin TZID, folded lines,
// escaped commas, -0 for single events and numeric suffixes for series dates.
const feedV1 = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//pretix//pretix//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-3uvxf-0@tickets.esn-germany.de\r\nSUMMARY:Freudenstadt City Trip\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261108T100000\r\nURL:https://tickets.esn-germany.de/esnstuttgart/3uvxf/\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-vgzcl-0@tickets.esn-germany.de\r\nSUMMARY:Ceramic Painting\\, round 2\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261110T180000\r\nURL:https://tickets.esn-germany.de/esnstuttgart/vgzcl/\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-ad3vj-2071@tickets.esn-germany.de\r\nSUMMARY:Stamm\r\n tisch\r\n" +
	"DTSTART:20261111T183000Z\r\nURL:https://tickets.esn-germany.de/esnstuttgart/ad3vj/2071/\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-jaqjb-0@tickets.esn-germany.de\r\nSUMMARY:Karaoke Night\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261112T200000\r\nURL:https://tickets.esn-germany.de/esnstuttgart/jaqjb/\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-old01-0@tickets.esn-germany.de\r\nSUMMARY:Pumpkin Festival\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261004T120000\r\nURL:https://tickets.esn-germany.de/esnstuttgart/old01/\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func withEvent(feed, uid, title, start, url string) string {
	ev := "BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + title + "\r\nDTSTART;TZID=Europe/Berlin:" + start +
		"\r\nURL:" + url + "\r\nEND:VEVENT\r\n"
	return strings.Replace(feed, "END:VCALENDAR", ev+"END:VCALENDAR", 1)
}

type fakeFetcher struct {
	ical   string
	widget string
	err    error
}

func (f *fakeFetcher) Get(_ context.Context, url string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if strings.Contains(url, "widget") {
		if f.widget == "" {
			return nil, errors.New("HTTP 404")
		}
		return []byte(f.widget), nil
	}
	return []byte(f.ical), nil
}

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, berlin)

func setup(t *testing.T) (Config, *fakeFetcher, *LogNotifier) {
	t.Helper()
	retryDelay = time.Millisecond
	return Config{ICalURL: "ical", WidgetURL: "widget", StatePath: filepath.Join(t.TempDir(), "state.json")},
		&fakeFetcher{ical: feedV1}, &LogNotifier{}
}

func run(t *testing.T, cfg Config, f Fetcher, n Notifier, at time.Time) error {
	t.Helper()
	return Run(context.Background(), cfg, f, n, at)
}

func TestParseICal(t *testing.T) {
	evs, err := ParseICal(feedV1)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[1].Title != "Ceramic Painting, round 2" {
		t.Errorf("escape: %q", evs[1].Title)
	}
	if evs[2].Title != "Stammtisch" {
		t.Errorf("folding: %q", evs[2].Title)
	}
	if got := evs[0].Start.UTC(); !got.Equal(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("TZID start: %v", got)
	}
	if got := evs[2].Start.UTC(); !got.Equal(time.Date(2026, 11, 11, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("UTC start: %v", got)
	}
}

func TestParseICalRejectsGarbage(t *testing.T) {
	if _, err := ParseICal("<html>Maintenance</html>"); err == nil {
		t.Error("HTML accepted as feed")
	}
	if _, err := ParseICal(feedV1[:len(feedV1)/2]); err == nil {
		t.Error("truncated feed accepted")
	}
}

func TestFirstRunSendsOneMessage(t *testing.T) {
	cfg, f, n := setup(t)
	if err := run(t, cfg, f, n, now); err != nil {
		t.Fatal(err)
	}
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Body, "Watching 4 upcoming") {
		t.Fatalf("first run sent %+v", n.Sent)
	}
	// Second run with an unchanged feed: silence.
	n.Sent = nil
	if err := run(t, cfg, f, n, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(n.Sent) != 0 {
		t.Fatalf("unchanged feed sent %+v", n.Sent)
	}
}

func TestNewEventAlertOnce(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil

	f.ical = withEvent(feedV1, "pretix-esnstuttgart-zzz11-0@x", "Black Forest Hike", "20261122T090000", "https://tickets.esn-germany.de/esnstuttgart/zzz11/")
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "Black Forest Hike") {
		t.Fatalf("got %+v", n.Sent)
	}
	if n.Sent[0].Click != "https://tickets.esn-germany.de/esnstuttgart/zzz11/" {
		t.Errorf("click: %q", n.Sent[0].Click)
	}

	n.Sent = nil
	run(t, cfg, f, n, now.Add(30*time.Minute))
	if len(n.Sent) != 0 {
		t.Fatalf("duplicate alert %+v", n.Sent)
	}
}

func TestFailedSendIsRetriedNextRun(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)

	f.ical = withEvent(feedV1, "pretix-esnstuttgart-zzz11-0@x", "Hike", "20261122T090000", "u")
	failing := &LogNotifier{Fail: true}
	if err := run(t, cfg, f, failing, now.Add(15*time.Minute)); err == nil {
		t.Fatal("send failure must surface as an error")
	}
	n.Sent = nil
	run(t, cfg, f, n, now.Add(30*time.Minute))
	if len(n.Sent) != 1 {
		t.Fatalf("lost notification after failed send: %+v", n.Sent)
	}
}

func TestOutageDoesNotCauseReAlerts(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil

	// Three failures → exactly one "broken" alert.
	f.err = errors.New("HTTP 503")
	for i := 1; i <= 4; i++ {
		if err := run(t, cfg, f, n, now.Add(time.Duration(i)*15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "can't read") {
		t.Fatalf("broken alerts: %+v", n.Sent)
	}

	// Recovery: one "recovered", no event alerts.
	f.err = nil
	n.Sent = nil
	run(t, cfg, f, n, now.Add(2*time.Hour))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "recovered") {
		t.Fatalf("recovery: %+v", n.Sent)
	}
}

func TestPartialFeedThenRestoreNoDuplicates(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil

	// Feed with only one upcoming event: rejected twice, then accepted.
	partial := "BEGIN:VCALENDAR\r\n" + feedV1[strings.Index(feedV1, "BEGIN:VEVENT"):strings.Index(feedV1, "BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-vgzcl")] + "END:VCALENDAR\r\n"
	f.ical = partial
	for i := 1; i <= 3; i++ {
		run(t, cfg, f, n, now.Add(time.Duration(i)*15*time.Minute))
	}
	// Full feed comes back: the "gone" events must not be announced as new.
	f.ical = feedV1
	run(t, cfg, f, n, now.Add(time.Hour))
	for _, s := range n.Sent {
		if strings.HasPrefix(s.Title, "New ESN event") {
			t.Fatalf("re-announced after partial feed: %+v", n.Sent)
		}
	}
}

func TestDeletedAndReaddedNotReannounced(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	f.ical = strings.Replace(feedV1, "UID:pretix-esnstuttgart-jaqjb-0", "UID:pretix-esnstuttgart-jaqjb-0-removed", 1)
	run(t, cfg, f, n, now.Add(15*time.Minute)) // karaoke "gone", a renamed UID appears
	n.Sent = nil
	f.ical = feedV1
	run(t, cfg, f, n, now.Add(30*time.Minute))
	if len(n.Sent) != 0 {
		t.Fatalf("re-added event announced again: %+v", n.Sent)
	}
}

const widgetSoldOut = `{"events":[
 {"name":"Freudenstadt City Trip","event_url":"https://tickets.esn-germany.de/esnstuttgart/3uvxf/","availability":{"color":"red","text":"Sold out"}},
 {"name":"Karaoke Night","event_url":"https://tickets.esn-germany.de/esnstuttgart/jaqjb/","availability":{"color":"none","text":"Sale not yet started"}}]}`

const widgetOpen = `{"events":[
 {"name":"Freudenstadt City Trip","event_url":"https://tickets.esn-germany.de/esnstuttgart/3uvxf/","availability":{"color":"orange","text":"Few tickets left"}},
 {"name":"Karaoke Night","event_url":"https://tickets.esn-germany.de/esnstuttgart/jaqjb","availability":{"color":"green","text":"Book now"}}]}`

func TestTicketsBecomeAvailable(t *testing.T) {
	cfg, f, n := setup(t)
	f.widget = widgetSoldOut
	run(t, cfg, f, n, now)
	n.Sent = nil

	f.widget = widgetOpen
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 2 {
		t.Fatalf("want 2 on-sale alerts, got %+v", n.Sent)
	}
	for _, s := range n.Sent {
		if s.Priority != 5 || !strings.HasPrefix(s.Title, "Tickets available") {
			t.Errorf("bad on-sale alert %+v", s)
		}
	}
	// Still on sale next run: silence. Widget down: silence, last state kept.
	n.Sent = nil
	run(t, cfg, f, n, now.Add(30*time.Minute))
	f.widget = ""
	run(t, cfg, f, n, now.Add(45*time.Minute))
	if len(n.Sent) != 0 {
		t.Fatalf("repeat on-sale alerts: %+v", n.Sent)
	}
}

func TestReschedule(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil
	f.ical = strings.Replace(feedV1, "20261112T200000", "20261119T200000", 1)
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.HasPrefix(n.Sent[0].Title, "Date changed") {
		t.Fatalf("got %+v", n.Sent)
	}
}

func TestPastEventsNeverAnnounced(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil
	f.ical = withEvent(feedV1, "pretix-esnstuttgart-past2-0@x", "Already happened", "20261020T180000", "u")
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 0 {
		t.Fatalf("past event announced: %+v", n.Sent)
	}
}

func TestManyNewEventsSummarised(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil
	feed := feedV1
	for i := 0; i < 7; i++ {
		feed = withEvent(feed, "pretix-esnstuttgart-ad3vj-30"+string(rune('0'+i))+"@x", "Stammtisch", "2026120"+string(rune('1'+i))+"T193000", "u")
	}
	f.ical = feed
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "7 new") {
		t.Fatalf("got %+v", n.Sent)
	}
}

// --- Regression tests for the review findings ---

func TestFailedFirstRunThenSuccessIsStillFirstRun(t *testing.T) {
	cfg, f, n := setup(t)
	f.err = errors.New("HTTP 503")
	if err := run(t, cfg, f, n, now); err == nil {
		t.Fatal("failed first run must surface as an error")
	}
	f.err = nil
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "is running") {
		t.Fatalf("expected only the welcome push, got %+v", n.Sent)
	}
}

func TestUnparseableDateStillAnnounced(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil
	f.ical = withEvent(feedV1, "pretix-esnstuttgart-bad01-0@x", "Mystery Trip", "garbage", "u")
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Body, "date TBA") {
		t.Fatalf("event with bad date missed: %+v", n.Sent)
	}
}

func TestVeryLongLineDoesNotTruncateFeed(t *testing.T) {
	long := "DESCRIPTION:" + strings.Repeat("x", 2<<20) + "\r\n"
	feed := strings.Replace(feedV1, "SUMMARY:Freudenstadt City Trip\r\n", "SUMMARY:Freudenstadt City Trip\r\n"+long, 1)
	evs, err := ParseICal(feed)
	if err != nil || len(evs) != 5 {
		t.Fatalf("got %d events, err %v", len(evs), err)
	}
}

func TestFlappingAvailabilityAlertsOnceWithinCooldown(t *testing.T) {
	cfg, f, n := setup(t)
	f.widget = widgetSoldOut
	run(t, cfg, f, n, now)
	n.Sent = nil
	for i := 1; i <= 8; i++ {
		if i%2 == 1 {
			f.widget = widgetOpen
		} else {
			f.widget = widgetSoldOut
		}
		run(t, cfg, f, n, now.Add(time.Duration(i)*15*time.Minute))
	}
	if len(n.Sent) != 2 { // one per event (Freudenstadt + Karaoke)
		t.Fatalf("flapping produced %d alerts: %+v", len(n.Sent), n.Sent)
	}
	// After the cooldown, a genuine re-opening alerts again.
	n.Sent = nil
	f.widget = widgetSoldOut
	run(t, cfg, f, n, now.Add(13*time.Hour))
	f.widget = widgetOpen
	run(t, cfg, f, n, now.Add(13*time.Hour+15*time.Minute))
	if len(n.Sent) != 2 { // both events are still upcoming and both re-opened
		t.Fatalf("after cooldown: %+v", n.Sent)
	}
}

func TestRecoveredRetriedIfSendFails(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	f.err = errors.New("down")
	for i := 1; i <= 3; i++ {
		run(t, cfg, f, n, now.Add(time.Duration(i)*15*time.Minute))
	}
	f.err = nil
	run(t, cfg, f, &LogNotifier{Fail: true}, now.Add(time.Hour))
	n.Sent = nil
	run(t, cfg, f, n, now.Add(75*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Title, "recovered") {
		t.Fatalf("recovered not retried: %+v", n.Sent)
	}
}

func TestSuspiciousStreakSurvivesNetworkBlip(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	partial := "BEGIN:VCALENDAR\r\n" + feedV1[strings.Index(feedV1, "BEGIN:VEVENT"):strings.Index(feedV1, "BEGIN:VEVENT\r\nUID:pretix-esnstuttgart-vgzcl")] + "END:VCALENDAR\r\n"
	n.Sent = nil
	f.ical = partial
	run(t, cfg, f, n, now.Add(15*time.Minute)) // suspicious 1
	f.err = errors.New("blip")
	run(t, cfg, f, n, now.Add(30*time.Minute)) // network
	f.err = nil
	run(t, cfg, f, n, now.Add(45*time.Minute)) // suspicious 2 → still rejected, fail streak 3 → alert
	run(t, cfg, f, n, now.Add(60*time.Minute)) // accepted
	st, _, _ := LoadState(cfg.StatePath)
	if st.FailStreak != 0 {
		t.Fatalf("shrunk feed never accepted, streak %d", st.FailStreak)
	}
}

func TestSummaryCapped(t *testing.T) {
	cfg, f, n := setup(t)
	run(t, cfg, f, n, now)
	n.Sent = nil
	feed := feedV1
	for i := 0; i < 40; i++ {
		feed = withEvent(feed, "pretix-esnstuttgart-s-"+strings.Repeat("a", i+1)+"@x", "Event", "20261201T193000", "u")
	}
	f.ical = feed
	run(t, cfg, f, n, now.Add(15*time.Minute))
	if len(n.Sent) != 1 || !strings.Contains(n.Sent[0].Body, "…and 15 more") || len(n.Sent[0].Body) > 4000 {
		t.Fatalf("summary: %d pushes, body len %d", len(n.Sent), len(n.Sent[0].Body))
	}
}
