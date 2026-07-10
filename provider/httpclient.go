package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// DefaultRequestTimeout bounds a single model call when request_timeout_seconds is
// not set. It is deliberately generous enough for large generations while still
// turning a hung connection into a prompt, actionable error instead of a process
// that blocks for the whole command timeout (up to an hour).
const DefaultRequestTimeout = 300 * time.Second

// HardenedTransport returns an *http.Transport that fails fast on a dead or
// half-open connection instead of blocking forever on a silent peer:
//
//   - a dial timeout and TLS-handshake timeout bound connection setup;
//   - TCP keep-alive probes surface a silently-gone peer as a read error (the
//     failure mode where the server completes but the client never sees EOF);
//   - a bounded idle-conn lifetime recycles pooled connections so the next call
//     does not reuse a stale flow.
//
// It intentionally does NOT set ResponseHeaderTimeout: a non-streaming model call
// may legitimately take minutes to return its first byte while the model
// generates, so the overall wall-clock bound is a per-request context deadline
// (see WithRequestTimeout), not a header timeout.
func HardenedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.IdleConnTimeout = 90 * time.Second
	t.ExpectContinueTimeout = 5 * time.Second
	return t
}

// HardenedHTTPClient returns an *http.Client using HardenedTransport with no
// overall Client.Timeout — callers bound each request with a context deadline
// (WithRequestTimeout), which also composes correctly with the AWS SDK's retry and
// streaming behavior.
func HardenedHTTPClient() *http.Client {
	return &http.Client{Transport: HardenedTransport()}
}

// ParseRequestTimeout reads request_timeout_seconds from settings, returning
// DefaultRequestTimeout when unset. It is the maximum wall-clock time for a single
// model call.
func ParseRequestTimeout(settings map[string]string) (time.Duration, error) {
	v := settings["request_timeout_seconds"]
	if v == "" {
		return DefaultRequestTimeout, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid request_timeout_seconds %q: must be a positive integer", v)
	}
	return time.Duration(n) * time.Second, nil
}

// WithRequestTimeout derives a per-call context bounded by d (falling back to
// DefaultRequestTimeout when d <= 0).
func WithRequestTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = DefaultRequestTimeout
	}
	return context.WithTimeout(ctx, d)
}
