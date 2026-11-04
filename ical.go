package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// Event is one bookable date: a single event, or one date of an event series.
type Event struct {
	UID   string
	Title string
	Start time.Time
	URL   string
}

var berlin = mustLoadBerlin()

func mustLoadBerlin() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		// Never fail hard on missing tzdata; UTC+1 is close enough for display.
		return time.FixedZone("CET", 3600)
	}
	return loc
}

// ParseICal extracts VEVENTs from an iCalendar document.
// It handles line folding, property parameters (e.g. DTSTART;TZID=...),
// and text escapes. Events without a UID are skipped.
func ParseICal(data string) ([]Event, error) {
	if !strings.Contains(data, "BEGIN:VCALENDAR") {
		return nil, fmt.Errorf("not an iCalendar document")
	}
	lines := unfold(data)

	var events []Event
	var cur *Event
	for _, line := range lines {
		name, params, value, ok := splitProp(line)
		if !ok {
			continue
		}
		switch {
		case name == "BEGIN" && value == "VEVENT":
			cur = &Event{}
		case name == "END" && value == "VEVENT":
			if cur != nil && cur.UID != "" {
				events = append(events, *cur)
			}
			cur = nil
		case cur == nil:
			continue
		case name == "UID":
			cur.UID = value
		case name == "SUMMARY":
			cur.Title = unescape(value)
		case name == "URL":
			cur.URL = value
		case name == "DTSTART":
			t, err := parseICalTime(value, params["TZID"])
			if err != nil {
				log.Printf("warning: unparseable DTSTART %q: %v (event will be treated as date TBA)", value, err)
			} else {
				cur.Start = t
			}
		}
	}
	if !strings.Contains(data, "END:VCALENDAR") {
		return nil, fmt.Errorf("truncated iCalendar document (no END:VCALENDAR)")
	}
	return events, nil
}

func unfold(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	var out []string
	// strings.Split instead of bufio.Scanner: no line-length limit that could
	// silently cut the feed short.
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(out) > 0 {
			out[len(out)-1] += line[1:]
			continue
		}
		out = append(out, line)
	}
	return out
}

// splitProp splits "NAME;P1=V1;P2=V2:value" into its parts.
func splitProp(line string) (name string, params map[string]string, value string, ok bool) {
	// The value starts at the first ':' that is not inside a quoted param value.
	inQuote := false
	colon := -1
	for i, r := range line {
		if r == '"' {
			inQuote = !inQuote
		} else if r == ':' && !inQuote {
			colon = i
			break
		}
	}
	if colon < 0 {
		return "", nil, "", false
	}
	head, value := line[:colon], line[colon+1:]
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	params = map[string]string{}
	for _, p := range parts[1:] {
		if k, v, found := strings.Cut(p, "="); found {
			params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
	}
	return name, params, value, true
}

func unescape(s string) string {
	r := strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`)
	return strings.TrimSpace(r.Replace(s))
}

func parseICalTime(v, tzid string) (time.Time, error) {
	loc := berlin
	if tzid != "" {
		if l, err := time.LoadLocation(tzid); err == nil {
			loc = l
		}
	}
	switch {
	case strings.HasSuffix(v, "Z"):
		return time.Parse("20060102T150405Z", v)
	case len(v) == 8:
		return time.ParseInLocation("20060102", v, loc)
	default:
		return time.ParseInLocation("20060102T150405", v, loc)
	}
}
