// esn-watch: push a phone notification when ESN Stuttgart lists a new event,
// when tickets for a sold-out / not-yet-on-sale event become available, or when
// an event's date changes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultICal   = "https://tickets.esn-germany.de/esnstuttgart/events/ical/?locale=en"
	defaultWidget = "https://tickets.esn-germany.de/esnstuttgart/widget/product_list?lang=en&style=list"

	brokenAfter     = 3                    // consecutive failed runs before a "watcher broken" alert
	keepAliveEvery  = 25 * 24 * time.Hour  // force a state commit so GitHub doesn't disable the schedule
	pruneAfter      = 120 * 24 * time.Hour // forget events this long after they've left the feed
	maxSingleAlerts = 5                    // more new events than this in one run → one summary push
	maxSummaryLines = 25                   // keep the summary under ntfy's 4 KB message limit
)

type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

type HTTPFetcher struct {
	Client    *http.Client
	UserAgent string
}

func (f *HTTPFetcher) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 5<<20))
}

type Config struct {
	ICalURL   string
	WidgetURL string // empty disables availability tracking
	StatePath string
}

// Run does one check. It returns an error only for problems the user must
// know about via GitHub's failure email (state file broken, notifications
// undeliverable). Feed outages are handled internally with the fail streak.
func Run(ctx context.Context, cfg Config, f Fetcher, nt Notifier, now time.Time) error {
	st, _, err := LoadState(cfg.StatePath)
	if err != nil {
		return fmt.Errorf("state file unreadable, refusing to continue (fix or delete %s): %w", cfg.StatePath, err)
	}

	feed, fetchErr := fetchFeed(ctx, f, cfg.ICalURL)
	suspicious := false
	if fetchErr == nil {
		fetchErr = sanityCheck(st, feed, now)
		suspicious = fetchErr != nil
	}
	if fetchErr != nil {
		if !st.Initialized {
			// Never save a half-initialised state: the next successful run must
			// still be treated as the first one, or it would announce everything.
			return fmt.Errorf("first run could not read the feed: %w", fetchErr)
		}
		return handleFetchFailure(ctx, cfg, st, nt, fetchErr, suspicious, now)
	}

	var sendErr error
	if st.BrokenAlert {
		if err := sendWithRetry(ctx, nt, Notification{
			Title: "ESN watcher recovered", Body: "The ticket shop is reachable again.", Priority: 2, Tags: []string{"white_check_mark"},
		}); err != nil {
			sendErr = err // keep BrokenAlert set so "recovered" is retried next run
		} else {
			st.BrokenAlert = false
		}
	}
	st.FailStreak, st.SuspiciousStreak, st.LastError = 0, 0, ""

	avail := fetchAvailability(ctx, f, cfg.WidgetURL)
	changes := Diff(st, feed, avail, now)

	if !st.Initialized {
		st.Initialized = true
		// First run: remember everything, announce nothing except that it works.
		for _, c := range changes {
			c.Apply(st)
		}
		n := st.ActiveUpcoming(now)
		if err := sendWithRetry(ctx, nt, Notification{
			Title: "ESN watcher is running",
			Body:  fmt.Sprintf("Watching %d upcoming events. You'll get a push when a new one is listed.", n),
			Click: "https://tickets.esn-germany.de/esnstuttgart/", Priority: 3, Tags: []string{"eyes"},
		}); err != nil {
			sendErr = err
		}
		log.Printf("first run: recorded %d events (%d upcoming)", len(st.Events), n)
	} else {
		if err := deliver(ctx, st, nt, changes); err != nil {
			sendErr = err
		}
	}

	st.Prune(now, pruneAfter)
	if now.Sub(st.Touched) > keepAliveEvery {
		st.Touched = now
	}
	if err := st.Save(cfg.StatePath); err != nil {
		return err
	}
	return sendErr
}

func deliver(ctx context.Context, st *State, nt Notifier, changes []Change) error {
	var newOnes, rest []Change
	for _, c := range changes {
		if c.Kind == ChangeNew {
			newOnes = append(newOnes, c)
		} else {
			rest = append(rest, c)
		}
	}

	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Ticket-availability and date changes always go out individually: they're rare and urgent.
	for _, c := range rest {
		err := sendWithRetry(ctx, nt, c.Message())
		if err == nil {
			c.Apply(st)
		}
		record(err)
	}

	if len(newOnes) > maxSingleAlerts {
		var lines []string
		for i, c := range newOnes {
			if i == maxSummaryLines {
				lines = append(lines, fmt.Sprintf("…and %d more", len(newOnes)-i))
				break
			}
			lines = append(lines, fmt.Sprintf("• %s (%s)", c.Event.Title, fmtWhen(c.Event.Start)))
		}
		err := sendWithRetry(ctx, nt, Notification{
			Title:    fmt.Sprintf("%d new ESN events", len(newOnes)),
			Body:     strings.Join(lines, "\n"),
			Click:    "https://tickets.esn-germany.de/esnstuttgart/?style=list",
			Priority: 4, Tags: []string{"tada"},
		})
		if err == nil {
			for _, c := range newOnes {
				c.Apply(st)
			}
		}
		record(err)
		return firstErr
	}

	for _, c := range newOnes {
		err := sendWithRetry(ctx, nt, c.Message())
		if err == nil {
			c.Apply(st)
		}
		record(err)
	}
	return firstErr
}

func fetchFeed(ctx context.Context, f Fetcher, url string) ([]Event, error) {
	body, err := f.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	return ParseICal(string(body))
}

// sanityCheck rejects feeds that look broken (empty, or most events vanished).
// Vanished events are only marked "gone", never deleted, so even an accepted
// bad feed can't cause duplicate alerts later. The check is a delay, not a
// wall: if the same suspicious result persists for acceptSuspiciousAfter runs
// it's treated as real (e.g. quiet semester break with no events listed).
const acceptSuspiciousAfter = 2

func sanityCheck(st *State, feed []Event, now time.Time) error {
	if !st.Initialized {
		if len(feed) == 0 {
			return errors.New("feed parsed but contains no events")
		}
		return nil
	}
	if st.SuspiciousStreak >= acceptSuspiciousAfter {
		return nil
	}
	prev := st.ActiveUpcoming(now)
	if len(feed) == 0 {
		if prev == 0 {
			return nil
		}
		return fmt.Errorf("suspicious feed: no events at all, previously %d upcoming", prev)
	}
	upcomingInFeed := 0
	for _, e := range feed {
		if isUpcoming(e.Start, now) {
			upcomingInFeed++
		}
	}
	if prev >= 4 && upcomingInFeed*2 < prev {
		return fmt.Errorf("suspicious feed: %d upcoming events, previously %d", upcomingInFeed, prev)
	}
	return nil
}

func handleFetchFailure(ctx context.Context, cfg Config, st *State, nt Notifier, fetchErr error, suspicious bool, now time.Time) error {
	st.FailStreak++
	if suspicious {
		st.SuspiciousStreak++
	}
	st.LastError = fetchErr.Error()
	log.Printf("feed problem (%d in a row): %v", st.FailStreak, fetchErr)

	var sendErr error
	if st.FailStreak >= brokenAfter && !st.BrokenAlert {
		err := sendWithRetry(ctx, nt, Notification{
			Title:    "ESN watcher can't read the ticket shop",
			Body:     fmt.Sprintf("Failed %d checks in a row: %s. Check the site yourself until it recovers.", st.FailStreak, fetchErr),
			Click:    "https://tickets.esn-germany.de/esnstuttgart/",
			Priority: 3, Tags: []string{"warning"},
		})
		if err == nil {
			st.BrokenAlert = true
		}
		sendErr = err
	}
	if now.Sub(st.Touched) > keepAliveEvery {
		st.Touched = now
	}
	if err := st.Save(cfg.StatePath); err != nil {
		return err
	}
	return sendErr
}

// fetchAvailability is best effort. nil means "unknown this run".
func fetchAvailability(ctx context.Context, f Fetcher, url string) map[string]string {
	if url == "" {
		return nil
	}
	body, err := f.Get(ctx, url)
	if err != nil {
		log.Printf("availability check skipped: %v", err)
		return nil
	}
	m := ParseWidget(body)
	if len(m) == 0 {
		log.Printf("availability check skipped: no usable data in widget response")
		return nil
	}
	return m
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print notifications instead of sending them; state is still written")
	testNotify := flag.Bool("test-notify", false, "send one test notification and exit")
	statePath := flag.String("state", "state.json", "path of the state file")
	flag.Parse()
	log.SetFlags(0)

	cfg := Config{
		ICalURL:   envOr("ESN_ICAL_URL", defaultICal),
		WidgetURL: envOr("ESN_WIDGET_URL", defaultWidget),
		StatePath: *statePath,
	}
	if os.Getenv("ESN_DISABLE_AVAILABILITY") == "1" {
		cfg.WidgetURL = ""
	}

	ua := "esn-watch/1.0 (personal event notifier; polls every 15 min)"
	if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
		ua += " +https://github.com/" + repo
	}
	client := &http.Client{Timeout: 20 * time.Second}
	fetcher := &HTTPFetcher{Client: client, UserAgent: ua}

	var nt Notifier = &LogNotifier{}
	if !*dryRun {
		topic := strings.TrimSpace(os.Getenv("NTFY_TOPIC"))
		if topic == "" {
			log.Fatal("NTFY_TOPIC is not set (add it as a repository secret)")
		}
		nt = &Ntfy{Server: envOr("NTFY_SERVER", "https://ntfy.sh"), Topic: topic, Token: strings.TrimSpace(os.Getenv("NTFY_TOKEN")), Client: client}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if *testNotify {
		err := nt.Send(ctx, Notification{Title: "ESN watcher test 🎉", Body: "Notifications work.", Priority: 3, Click: "https://tickets.esn-germany.de/esnstuttgart/"})
		if err != nil {
			log.Fatal(err)
		}
		log.Print("test notification sent")
		return
	}

	if err := Run(ctx, cfg, fetcher, nt, time.Now()); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
