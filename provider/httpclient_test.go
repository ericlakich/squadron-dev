package provider

import (
	"context"
	"testing"
	"time"
)

func TestHardenedTransportTimeouts(t *testing.T) {
	tr := HardenedTransport()
	if tr.DialContext == nil {
		t.Error("DialContext should be set (custom dialer with keep-alive)")
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %s, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %s, want 90s", tr.IdleConnTimeout)
	}
	if tr.ExpectContinueTimeout != 5*time.Second {
		t.Errorf("ExpectContinueTimeout = %s, want 5s", tr.ExpectContinueTimeout)
	}
	// ResponseHeaderTimeout must stay unset: a non-streaming model call may take
	// minutes to return its first byte while generating.
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %s, want 0 (unset)", tr.ResponseHeaderTimeout)
	}
}

func TestHardenedHTTPClientHasNoOverallTimeout(t *testing.T) {
	c := HardenedHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("Client.Timeout = %s, want 0 (bounded per-request by context)", c.Timeout)
	}
	if c.Transport == nil {
		t.Error("Transport should be set")
	}
}

func TestParseRequestTimeout(t *testing.T) {
	if d, err := ParseRequestTimeout(map[string]string{}); err != nil || d != DefaultRequestTimeout {
		t.Errorf("default = %s, %v; want %s", d, err, DefaultRequestTimeout)
	}
	if d, err := ParseRequestTimeout(map[string]string{"request_timeout_seconds": "45"}); err != nil || d != 45*time.Second {
		t.Errorf("override = %s, %v; want 45s", d, err)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := ParseRequestTimeout(map[string]string{"request_timeout_seconds": bad}); err == nil {
			t.Errorf("expected error for request_timeout_seconds=%q", bad)
		}
	}
}

func TestWithRequestTimeout(t *testing.T) {
	ctx, cancel := WithRequestTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 2*time.Second+time.Second {
		t.Errorf("deadline remaining = %s, want ~2s", remaining)
	}

	// d <= 0 falls back to the default.
	ctx2, cancel2 := WithRequestTimeout(context.Background(), 0)
	defer cancel2()
	dl2, _ := ctx2.Deadline()
	if remaining := time.Until(dl2); remaining < DefaultRequestTimeout-5*time.Second {
		t.Errorf("default deadline remaining = %s, want ~%s", remaining, DefaultRequestTimeout)
	}
}

func TestHeartbeatStops(t *testing.T) {
	stop := Heartbeat("test")
	// Should return promptly and not panic; the goroutine exits on stop.
	stop()
}
