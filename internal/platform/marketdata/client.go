// Package marketdata fetches daily prices from Yahoo and exchange rates from ČNB; it
// is the only package that reaches the network for market data (ADR 209).
package marketdata

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rotisserie/eris"
)

const (
	maxResponseBytes = 10 << 20
	requestTimeout   = 30 * time.Second
	// Yahoo throttles by volume from one address; two requests a second stays clear of it.
	minRequestGap = 500 * time.Millisecond
	retryBackoff  = 2 * time.Second
	maxRetryWait  = 30 * time.Second
)

var ErrRateLimited = eris.New("marketdata: rate limited")

// client is a polite HTTP getter: its own User-Agent, paced requests, a capped body,
// and one retry after a 429 or 5xx that honours Retry-After.
type client struct {
	http      *http.Client
	userAgent string

	mu   sync.Mutex
	last time.Time
}

func newClient(userAgent string) *client {
	return &client{http: &http.Client{Timeout: requestTimeout}, userAgent: userAgent}
}

type response struct {
	status     int
	body       []byte
	retryAfter time.Duration
}

func (c *client) get(ctx context.Context, url string) (*response, error) {
	res, err := c.once(ctx, url)
	if err == nil && !retryable(res.status) {
		return res, nil
	}
	wait := retryBackoff
	if res != nil {
		wait = retryAfter(res, wait)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(wait):
	}
	res, err = c.once(ctx, url)
	if err != nil {
		return nil, err
	}
	if res.status == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	return res, nil
}

func (c *client) once(ctx context.Context, url string) (*response, error) {
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, eris.Wrap(err, "build request")
	}
	req.Header.Set("User-Agent", c.userAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, eris.Wrap(err, "request")
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, eris.Wrap(err, "read response")
	}
	if len(body) > maxResponseBytes {
		return nil, eris.Errorf("response larger than %d bytes", maxResponseBytes)
	}
	r := &response{status: res.StatusCode, body: body}
	if s := res.Header.Get("Retry-After"); s != "" {
		if seconds, err := strconv.Atoi(s); err == nil {
			r.retryAfter = time.Duration(seconds) * time.Second
		}
	}
	return r, nil
}

func (c *client) pace(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Until(c.last.Add(minRequestGap))
	if wait < 0 {
		wait = 0
	}
	c.last = time.Now().Add(wait)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func retryAfter(res *response, fallback time.Duration) time.Duration {
	if res.retryAfter <= 0 {
		return fallback
	}
	return min(res.retryAfter, maxRetryWait)
}
