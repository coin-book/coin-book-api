package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"coin-book-api/internal/transport/http/apierr"
)

type windowLimiter struct {
	mu        sync.Mutex
	perWindow int
	window    time.Duration
	started   time.Time
	counts    map[string]int
}

func (l *windowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.started) >= l.window {
		l.started, l.counts = now, map[string]int{}
	}
	l.counts[key]++
	return l.counts[key] <= l.perWindow
}

// RateLimitMiddleware throttles credential operations per client address.
func RateLimitMiddleware(api huma.API, perMinute int) func(huma.Context, func(huma.Context)) {
	limiter := &windowLimiter{perWindow: perMinute, window: time.Minute, counts: map[string]int{}}
	return func(ctx huma.Context, next func(huma.Context)) {
		if limited, _ := ctx.Operation().Metadata[metaRateLimit].(bool); limited {
			host, _, err := net.SplitHostPort(ctx.RemoteAddr())
			if err != nil {
				host = ctx.RemoteAddr()
			}
			if !limiter.allow(host, time.Now()) {
				writeErr(api, ctx, apierr.New(http.StatusTooManyRequests, apierr.CodeRateLimited))
				return
			}
		}
		next(ctx)
	}
}
