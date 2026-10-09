package eventcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	httpTimeout  = 60 * time.Second
	maxBodyBytes = 4 << 20
)

// Client talks to event-center over HTTP. URL empty: EVENT_CENTER_API_URL,
// else DefaultAPIURL. Token empty: EVENT_CENTER_ADMIN_TOKEN, else EVENTD_ADMIN_TOKEN.
type Client struct {
	URL    string
	Token  string
	HTTP   *http.Client
	Stream string
}

// New is the client as the runtime wires it.
func New() *Client { return &Client{} }

func (c *Client) BaseURL() string {
	if c != nil && strings.TrimSpace(c.URL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.URL), "/")
	}
	if v := strings.TrimSpace(os.Getenv(EnvAPIURL)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}

func (c *Client) token() string {
	if c != nil && strings.TrimSpace(c.Token) != "" {
		return strings.TrimSpace(c.Token)
	}
	if v := strings.TrimSpace(os.Getenv(EnvAdminToken)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(EnvAdminTokenAlias))
}

func (c *Client) stream() string {
	if c != nil && strings.TrimSpace(c.Stream) != "" {
		return strings.TrimSpace(c.Stream)
	}
	return StreamGitHub
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: httpTimeout}
}

// Streams is GET /v1/streams.
func (c *Client) Streams(ctx context.Context) (StreamsResult, error) {
	var out StreamsResult
	if err := c.getJSON(ctx, "/v1/streams", nil, &out); err != nil {
		return StreamsResult{}, err
	}
	if out.Streams == nil {
		out.Streams = map[string]int64{}
	}
	return out, nil
}

// Pull is GET /v1/streams/{stream}/events.
func (c *Client) Pull(ctx context.Context, stream string, after, limit int64, wait time.Duration) (ListResult, error) {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		stream = c.stream()
	}
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	if limit > 0 {
		q.Set("limit", strconv.FormatInt(limit, 10))
	}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	var out ListResult
	path := "/v1/streams/" + url.PathEscape(stream) + "/events"
	if err := c.getJSON(ctx, path, q, &out); err != nil {
		return ListResult{}, err
	}
	if out.Events == nil {
		out.Events = []Event{}
	}
	return out, nil
}

// Recent returns the last lookback events of stream, oldest first. An unknown
// stream is an empty list, not an error: event-center has not seen that producer yet.
func (c *Client) Recent(ctx context.Context, stream string, lookback int) ([]Event, error) {
	if lookback <= 0 {
		lookback = DefaultLookback
	}
	stream = strings.TrimSpace(stream)
	if stream == "" {
		stream = c.stream()
	}
	heads, err := c.Streams(ctx)
	if err != nil {
		return nil, err
	}
	head := heads.Streams[stream]
	if stream == StreamAll {
		head = heads.GlobalSeq
	}
	if head <= 0 {
		return []Event{}, nil
	}
	after := head - int64(lookback)
	if after < 0 {
		after = 0
	}
	res, err := c.Pull(ctx, stream, after, int64(lookback), 0)
	if err != nil {
		return nil, err
	}
	return res.Events, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	raw := c.BaseURL() + path
	if len(q) > 0 {
		raw += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("event-center %s: %w", raw, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("event-center %s: %w", raw, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("event-center %s: http %d: %s", raw, resp.StatusCode, snippet(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("event-center %s: decode: %w", raw, err)
	}
	return nil
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(empty body)"
	}
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
