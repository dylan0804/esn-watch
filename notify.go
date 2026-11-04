package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Notification struct {
	Title    string
	Body     string
	Click    string
	Priority int
	Tags     []string
}

type Notifier interface {
	Send(ctx context.Context, n Notification) error
}

// Ntfy publishes via ntfy's JSON API, which handles UTF-8 and emoji in titles
// correctly (HTTP headers don't).
type Ntfy struct {
	Server string // e.g. https://ntfy.sh
	Topic  string
	Token  string // optional access token for a protected topic / self-hosted server
	Client *http.Client
}

func (n *Ntfy) Send(ctx context.Context, msg Notification) error {
	payload := map[string]any{
		"topic":   n.Topic,
		"title":   msg.Title,
		"message": msg.Body,
	}
	if msg.Body == "" {
		payload["message"] = msg.Title
	}
	if msg.Click != "" {
		payload["click"] = msg.Click
	}
	if msg.Priority != 0 {
		payload["priority"] = msg.Priority
	}
	if len(msg.Tags) > 0 {
		payload["tags"] = msg.Tags
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.Server, "/")+"/", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	resp, err := n.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// Deliberately don't include the topic in the error: logs of a public repo are public.
		return fmt.Errorf("ntfy: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// LogNotifier prints instead of sending (dry runs, tests).
type LogNotifier struct {
	Sent []Notification
	Fail bool
}

func (l *LogNotifier) Send(_ context.Context, n Notification) error {
	if l.Fail {
		return fmt.Errorf("simulated send failure")
	}
	l.Sent = append(l.Sent, n)
	fmt.Printf("[notify p%d] %s — %s (%s)\n", n.Priority, n.Title, n.Body, n.Click)
	return nil
}

var retryDelay = 2 * time.Second

func sendWithRetry(ctx context.Context, nt Notifier, n Notification) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = nt.Send(ctx, n); err == nil {
			return nil
		}
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt+1) * retryDelay):
		}
	}
	return err
}
