package httpapi

import (
	"net"
	"net/http"
	"strings"
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

// RateLimitMiddleware throttles credential operations per client address. Behind a
// reverse proxy every request arrives from the proxy, so clientIPHeader names the
// header the proxy sets to the one address it determined (X-Forwarded-For from Caddy's
// {client_ip}); empty means the connection's address.
func RateLimitMiddleware(api huma.API, perMinute int, clientIPHeader string) func(huma.Context, func(huma.Context)) {
	limiter := &windowLimiter{perWindow: perMinute, window: time.Minute, counts: map[string]int{}}
	return func(ctx huma.Context, next func(huma.Context)) {
		if limited, _ := ctx.Operation().Metadata[metaRateLimit].(bool); limited {
			if !limiter.allow(clientAddress(ctx, clientIPHeader), time.Now()) {
				writeErr(api, ctx, apierr.New(http.StatusTooManyRequests, apierr.CodeRateLimited))
				return
			}
		}
		next(ctx)
	}
}

func clientAddress(ctx huma.Context, header string) string {
	if header != "" {
		// A proxy appends to the right; the last entry is the one it wrote, never the client's.
		entries := strings.Split(ctx.Header(header), ",")
		if ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1])); ip != nil {
			return bucketOf(ip)
		}
	}
	host, _, err := net.SplitHostPort(ctx.RemoteAddr())
	if err != nil {
		return ctx.RemoteAddr()
	}
	if ip := net.ParseIP(host); ip != nil {
		return bucketOf(ip)
	}
	return host
}

// ipv6SiteBits: a home or cloud IPv6 client owns a whole /64 and could use a fresh
// address per request, so the limit applies to the prefix.
const ipv6SiteBits = 64

func bucketOf(ip net.IP) string {
	if ip.To4() != nil {
		return ip.String()
	}
	return ip.Mask(net.CIDRMask(ipv6SiteBits, 128)).String()
}
