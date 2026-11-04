package main

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Availability states we care about, normalised from pretix's widget colours.
const (
	AvailUnknown   = ""            // couldn't determine (endpoint failed, series row, ...)
	AvailOnSale    = "on_sale"     // green / orange: you can buy now
	AvailSoldOut   = "sold_out"    // red
	AvailNotOnSale = "not_on_sale" // none: listed, but presale not started / ended
)

// ParseWidget parses pretix's widget product_list JSON (style=list) into a map
// from event URL path (e.g. "/esnstuttgart/3uvxf/") to availability.
//
// This endpoint is what pretix's embeddable widget calls. It is not a documented
// API, so parsing is deliberately defensive: anything unexpected just means
// "unknown" and the watcher still works off the iCal feed.
func ParseWidget(data []byte) map[string]string {
	var doc struct {
		Events []struct {
			EventURL     string `json:"event_url"`
			Availability struct {
				Color string `json:"color"`
				Text  string `json:"text"`
			} `json:"availability"`
		} `json:"events"`
	}
	out := map[string]string{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out
	}
	for _, e := range doc.Events {
		if e.EventURL == "" {
			continue
		}
		if a := normaliseAvail(e.Availability.Color, e.Availability.Text); a != AvailUnknown {
			out[urlPath(e.EventURL)] = a
		}
	}
	return out
}

func normaliseAvail(color, text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "sold out"):
		return AvailSoldOut
	case strings.Contains(t, "not yet") || strings.Contains(t, "no longer"):
		return AvailNotOnSale
	}
	switch strings.ToLower(color) {
	case "green", "orange":
		return AvailOnSale
	case "red":
		return AvailSoldOut
	case "none":
		return AvailNotOnSale
	}
	return AvailUnknown
}

// urlPath reduces an absolute or relative URL to its path with a trailing slash,
// so "https://host/esnstuttgart/3uvxf" and "/esnstuttgart/3uvxf/" compare equal.
func urlPath(raw string) string {
	p := raw
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		p = u.Path
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func availLabel(a string) string {
	switch a {
	case AvailOnSale:
		return "tickets on sale"
	case AvailSoldOut:
		return "sold out"
	case AvailNotOnSale:
		return "not on sale yet"
	}
	return ""
}
