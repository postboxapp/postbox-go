package postbox

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Event is a single server-sent event. Data is the raw data payload (data lines
// joined by newlines); decode it with encoding/json as needed.
type Event struct {
	ID    string
	Event string
	Data  string
}

// parsedEvent is the intermediate result of parsing one SSE record.
type parsedEvent struct {
	id       string
	event    string
	data     string
	hasData  bool
	retryMs  float64
	hasRetry bool
}

// streamEvents opens the SSE stream at /events and invokes handler once per
// event. It reconnects on transport drops and non-fatal server errors, resuming
// from Last-Event-ID, with full-jitter exponential backoff. A handler returning
// a non-nil error, a fatal (<500, non-429) server response, or context
// cancellation stops the stream.
func streamEvents(c *client, ctx context.Context, query map[string]string, handler func(Event) error) error {
	u := buildStreamURL(c.baseURL, "/events", query)
	lastEventID := ""
	reconnectDelay := backoffBaseMs
	attempt := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		reconnect, retry, err := c.runStream(ctx, u, lastEventID, &lastEventID, &reconnectDelay, handler)
		if err != nil && !reconnect {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if retry > 0 {
			reconnectDelay = retry
		}

		attempt++
		exp := attempt - 1
		if exp > 5 {
			exp = 5
		}
		delay := reconnectDelay
		for i := 0; i < exp; i++ {
			delay *= 2
		}
		if delay > backoffCapMs {
			delay = backoffCapMs
		}
		wait := time.Duration(randFloat()*delay) * time.Millisecond
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// runStream performs a single connection. It returns (reconnect, retryMs, err):
// reconnect=true means the caller should back off and retry; retryMs carries a
// server-suggested reconnect delay; a non-nil err with reconnect=false is fatal.
func (c *client) runStream(ctx context.Context, u, lastEventID string, lastOut *string, reconnectDelay *float64, handler func(Event) error) (bool, float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range c.defaultHeaders {
		req.Header.Set(k, v)
	}
	if c.projectID != "" {
		req.Header.Set("X-Project-Id", c.projectID)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Transport error: reconnect unless the caller cancelled.
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		return true, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		apiErr := errorFromResponse(resp.StatusCode, body, resp.Header.Get("X-Request-Id"), resp.Header.Get("Retry-After"))
		// Non-retryable server errors (auth, not found) terminate the stream.
		if apiErr.Status < 500 && apiErr.Status != http.StatusTooManyRequests {
			return false, 0, apiErr
		}
		return true, 0, apiErr
	}

	var buffer []byte
	chunk := make([]byte, 4096)
	var suggestedRetry float64
	for {
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
			for {
				start, end := findEventBoundary(buffer)
				if start < 0 {
					break
				}
				raw := string(buffer[:start])
				buffer = buffer[end:]
				parsed := parseSSE(raw)
				if parsed.id != "" {
					*lastOut = parsed.id
				}
				if parsed.hasRetry {
					suggestedRetry = parsed.retryMs
					*reconnectDelay = parsed.retryMs
				}
				if parsed.hasData {
					if herr := handler(Event{ID: parsed.id, Event: parsed.event, Data: parsed.data}); herr != nil {
						return false, suggestedRetry, herr
					}
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return true, suggestedRetry, nil
			}
			if ctx.Err() != nil {
				return false, suggestedRetry, ctx.Err()
			}
			return true, suggestedRetry, rerr
		}
	}
}

// findEventBoundary locates the first SSE record separator (\r\n\r\n, \n\n, or
// \r\r) and returns its start and end byte offsets, or (-1, -1) if none.
func findEventBoundary(buf []byte) (int, int) {
	best := -1
	bestLen := 0
	for _, sep := range []string{"\r\n\r\n", "\n\n", "\r\r"} {
		idx := indexOf(buf, sep)
		if idx >= 0 && (best == -1 || idx < best) {
			best = idx
			bestLen = len(sep)
		}
	}
	if best < 0 {
		return -1, -1
	}
	return best, best + bestLen
}

func indexOf(buf []byte, sep string) int {
	return strings.Index(string(buf), sep)
}

// parseSSE parses one raw SSE record into its fields.
func parseSSE(raw string) parsedEvent {
	out := parsedEvent{}
	var dataLines []string
	for _, line := range splitLines(raw) {
		if line == "" || strings.HasPrefix(line, ":") {
			continue // blank line or comment
		}
		idx := strings.Index(line, ":")
		var field, val string
		if idx == -1 {
			field = line
		} else {
			field = line[:idx]
			val = line[idx+1:]
		}
		if strings.HasPrefix(val, " ") {
			val = val[1:]
		}
		switch field {
		case "id":
			out.id = val
		case "event":
			out.event = val
		case "data":
			dataLines = append(dataLines, val)
		case "retry":
			if ms, err := strconv.ParseFloat(val, 64); err == nil {
				out.retryMs = ms
				out.hasRetry = true
			}
		}
	}
	if len(dataLines) > 0 {
		out.data = strings.Join(dataLines, "\n")
		out.hasData = true
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// buildStreamURL joins base + path and appends a simple string query map.
func buildStreamURL(baseURL, path string, query map[string]string) string {
	u := baseURL
	if strings.HasPrefix(path, "/") {
		u += path
	} else {
		u += "/" + path
	}
	if len(query) == 0 {
		return u
	}
	values := url.Values{}
	for k, v := range query {
		values.Add(k, v)
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + values.Encode()
}
