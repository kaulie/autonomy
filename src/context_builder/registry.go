package context_builder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Every resolver reads a registry the same way: one bounded GET into the shape it
// expects. A registry is a lookup, not a dependency — what it answers is cached for a
// few seconds and its absence is a thinner section, never an error a caller sees.

const (
	// defaultTTL is how long a successful read is reused. A registry changes when
	// someone creates or renames something, not per decision cycle.
	defaultTTL = 30 * time.Second
	// defaultFailureTTL is how long a failed read is remembered: long enough not to
	// hammer a service that is down, short enough to pick it up again quickly.
	defaultFailureTTL = 5 * time.Second
	// httpTimeout is the client's own ceiling; a build's timeout is usually shorter.
	httpTimeout = 5 * time.Second
)

func defaultHTTPClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// fetchJSON is one registry read.
func fetchJSON(ctx context.Context, client *http.Client, url string, out any) error {
	if client == nil {
		client = defaultHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return strings.TrimRight(value, "/")
	}
	return fallback
}

// cache is one registry read, remembered: what it answered, when, and whether it
// answered at all (a failure is remembered for the shorter TTL).
type cache struct {
	at   time.Time
	ok   bool
	data any
}

// fresh reports whether what is cached may be used, given the two TTLs.
func (c *cache) fresh(ttl, failureTTL time.Duration) bool {
	if c.at.IsZero() {
		return false
	}
	if c.ok {
		if ttl <= 0 {
			ttl = defaultTTL
		}
		return time.Since(c.at) < ttl
	}
	if failureTTL <= 0 {
		failureTTL = defaultFailureTTL
	}
	return time.Since(c.at) < failureTTL
}

// stringField reads a string out of a resolved section (a field an earlier resolver
// wrote, which the builder merges as a plain map).
func stringField(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
