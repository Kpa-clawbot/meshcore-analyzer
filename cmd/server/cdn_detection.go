package main

// Issue #1561: detect CDN-fronted deployments and warn ONCE.
//
// When operators put CoreScope behind Cloudflare/Fastly without
// configuring a /api/* cache bypass, dashboards go stale — the origin
// emits Cache-Control: no-store (#1551), but the CDN's zone-level
// caching policy can still cache JSON responses for hours
// (cf-cache-status: HIT, age > 0). We can't fix the CDN config from
// the server side; the best we can do is detect the situation and
// loudly tell the operator at the logs.
//
// Detection: presence of any CDN-specific request header
// (CF-Connecting-IP, CF-Ray, Fastly-Client-IP, True-Client-IP).
// We deliberately exclude X-Forwarded-For and X-Real-IP: every
// generic reverse proxy (nginx, Caddy, Traefik, k8s ingress) sets
// those, so including them would warn operators who aren't behind
// a CDN at all and train them to ignore the warning entirely
// (defeating the point of #1561).
//
// Side effects: a single log line per process boot — never blocks
// the request, never modifies the response, never logs again.

import (
	"log"
	"net/http"
	"sync"
	"sync/atomic"
)

var cdnWarnOnce sync.Once

// cdnWarned is set true after the first CDN-fronted request has been
// observed and logged. Subsequent requests short-circuit before the
// per-request header scan in firstCDNHeader — a hot-path optimization
// for the steady state (warning already emitted, every /api request
// otherwise pays for 4 http.Header.Get lookups forever).
var cdnWarned atomic.Bool

// cdnHeaders are HTTP request headers injected ONLY by CDNs
// (Cloudflare, Fastly, Akamai) — never by a generic reverse proxy.
// Detected case-insensitively by http.Header.Get.
//
// X-Forwarded-For / X-Real-IP are intentionally NOT in this list:
// every nginx/Caddy/Traefik/k8s-ingress deployment sets them, so
// using them as a CDN signal produces a false positive on every
// reverse-proxied install (issue #1561 round-1 review).
var cdnHeaders = []string{
	"CF-Connecting-IP", // Cloudflare
	"CF-Ray",           // Cloudflare
	"Fastly-Client-IP", // Fastly
	"True-Client-IP",   // Akamai (also set by Cloudflare Enterprise)
}

// cdnDetectionMiddleware inspects each incoming request for CDN
// headers and, on the FIRST one observed, logs a single warning
// pointing the operator at docs/deployment-behind-cdn.md. The
// middleware always calls next; it never blocks or rewrites.
func cdnDetectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast path: once we've warned, skip the per-request header
		// scan entirely. Steady state for any CDN-fronted deploy is
		// ~every request hitting this branch.
		if cdnWarned.Load() {
			next.ServeHTTP(w, r)
			return
		}
		if hdr := firstCDNHeader(r.Header); hdr != "" {
			cdnWarnOnce.Do(func() {
				log.Printf("[security] WARNING: detected request via CDN (%s header present). "+
					"Ensure /api/* is bypassed in your CDN config — see docs/deployment-behind-cdn.md. "+
					"Cached API responses cause observer-flap and incorrect dashboards.", hdr)
				cdnWarned.Store(true)
			})
		}
		next.ServeHTTP(w, r)
	})
}

func firstCDNHeader(h http.Header) string {
	for _, name := range cdnHeaders {
		if h.Get(name) != "" {
			return name
		}
	}
	return ""
}
