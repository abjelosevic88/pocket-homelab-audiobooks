// Package notify pushes a message to ntfy, an Apprise gateway or a plain
// JSON webhook. Pocket Audiobooks uses it for finished books and the weekly recap.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends to one destination.
type Client struct {
	URL    string
	Format string // ntfy | apprise | json
	HTTP   *http.Client
}

// New creates a client; an empty URL means notifications are off.
func New(url, format string) *Client {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = Guess(url)
	}
	return &Client{URL: strings.TrimSpace(url), Format: format, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Guess picks a format from the URL shape.
func Guess(url string) string {
	u := strings.ToLower(url)
	switch {
	case strings.Contains(u, "/notify/"):
		return "apprise"
	case strings.Contains(u, "ntfy"):
		return "ntfy"
	}
	return "json"
}

// Configured reports whether a URL is set.
func (c *Client) Configured() bool { return c != nil && c.URL != "" }

// Message is one push.
type Message struct {
	Title    string
	Body     string
	Priority string // info | warning | failure | success
	Tags     []string
	Link     string
}

// Send delivers the message.
func (c *Client) Send(ctx context.Context, m Message) error {
	if !c.Configured() {
		return errors.New("notifications are not configured")
	}
	var req *http.Request
	var err error
	switch c.Format {
	case "ntfy":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.URL, strings.NewReader(m.Body))
		if err != nil {
			return err
		}
		req.Header.Set("Title", m.Title)
		if len(m.Tags) > 0 {
			req.Header.Set("Tags", strings.Join(m.Tags, ","))
		}
		switch m.Priority {
		case "failure":
			req.Header.Set("Priority", "high")
		case "warning":
			req.Header.Set("Priority", "default")
		default:
			req.Header.Set("Priority", "low")
		}
		if m.Link != "" {
			req.Header.Set("Click", m.Link)
		}
	case "apprise":
		t := m.Priority
		if t == "" {
			t = "info"
		}
		body := m.Body
		if m.Link != "" {
			body += "\n" + m.Link
		}
		b, _ := json.Marshal(map[string]string{"title": m.Title, "body": body, "type": t})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	default:
		b, _ := json.Marshal(map[string]any{"title": m.Title, "body": m.Body, "priority": m.Priority, "tags": m.Tags, "link": m.Link, "source": "pocket-kitchen"})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		// Only the status: the answer of whatever the URL points at is never echoed.
		return fmt.Errorf("notify HTTP %d", res.StatusCode)
	}
	return nil
}
