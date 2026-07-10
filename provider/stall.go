package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStalled is the sentinel returned when a streaming model call is cancelled
// because no stream events arrived for stall_detection_seconds. Callers use
// errors.Is(err, ErrStalled) to distinguish a stall from a deadline or other
// error.
var ErrStalled = errors.New("bedrock stalled: no stream activity")

// DefaultStallTimeout is the idle-event window after which a streaming call is
// treated as dead. It is safe only because responses stream: a gap this long
// between tokens means the peer is gone, not that the model is still thinking.
const DefaultStallTimeout = 90 * time.Second

const (
	stallPollInterval = 15 * time.Second
	heartbeatInterval = 30 * time.Second
)

// ParseStallTimeout reads stall_detection_seconds (default 90). 0 disables stall
// cancellation (heartbeats still emit).
func ParseStallTimeout(settings map[string]string) (time.Duration, error) {
	v := settings["stall_detection_seconds"]
	if v == "" {
		return DefaultStallTimeout, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid stall_detection_seconds %q: must be a non-negative integer (0 disables)", v)
	}
	return time.Duration(n) * time.Second, nil
}

// Guard applies idle-event stall detection and heartbeat logging to streaming
// model calls.
type Guard struct {
	idle time.Duration
	log  *EventLog
}

// NewGuard builds a Guard. A nil log falls back to stderr-only logging.
func NewGuard(idle time.Duration, log *EventLog) *Guard {
	if log == nil {
		log = &EventLog{}
	}
	return &Guard{idle: idle, log: log}
}

// Start begins a guarded call: it returns a derived context that is cancelled
// (with cause ErrStalled) if no stream event is reported for the idle window, and
// a Call the caller drives. The caller must invoke Call.Touch for every stream
// event and Call.Finish when the call completes.
func (g *Guard) Start(ctx context.Context, session string) (context.Context, *Call) {
	cctx, cancel := context.WithCancelCause(ctx)
	c := &Call{g: g, session: session, cancel: cancel, started: time.Now(), done: make(chan struct{})}
	c.lastAt.Store(time.Now().UnixNano())
	go c.watch()
	return cctx, c
}

// Call is one guarded, in-flight streaming call.
type Call struct {
	g       *Guard
	session string
	cancel  context.CancelCauseFunc
	started time.Time
	done    chan struct{}
	once    sync.Once
	lastAt  atomic.Int64 // unix nano of the last event
	bytesIn atomic.Int64
	stalled atomic.Bool
}

// Touch records that a stream event of n bytes arrived, resetting the idle timer.
func (c *Call) Touch(n int) {
	if n > 0 {
		c.bytesIn.Add(int64(n))
	}
	c.lastAt.Store(time.Now().UnixNano())
}

// Stalled reports whether the call was cancelled by stall detection.
func (c *Call) Stalled() bool { return c.stalled.Load() }

// Err returns a formatted stall error, for use when Stalled() is true.
func (c *Call) Err() error {
	return fmt.Errorf("%w for %s (elapsed %s)", ErrStalled, c.g.idle.Round(time.Second), time.Since(c.started).Round(time.Second))
}

// Finish stops the watcher and releases the context. Safe to call once.
func (c *Call) Finish() {
	c.once.Do(func() {
		close(c.done)
		c.cancel(nil)
	})
}

func (c *Call) watch() {
	// Poll at least as often as the idle threshold so detection latency stays near
	// stall_detection_seconds (and small thresholds remain responsive).
	poll := stallPollInterval
	if c.g.idle > 0 && c.g.idle < poll {
		poll = c.g.idle
	}
	if poll < time.Second {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	lastBeat := c.started
	for {
		select {
		case <-c.done:
			return
		case now := <-ticker.C:
			idle := now.Sub(time.Unix(0, c.lastAt.Load()))
			elapsed := now.Sub(c.started)
			if c.g.idle > 0 && idle >= c.g.idle {
				c.stalled.Store(true)
				c.g.log.Stalled(c.session, idle, c.g.idle)
				c.cancel(ErrStalled)
				return
			}
			if now.Sub(lastBeat) >= heartbeatInterval {
				c.g.log.Heartbeat(c.session, elapsed, c.bytesIn.Load(), idle)
				lastBeat = now
			}
		}
	}
}

// EventLog appends heartbeat and stall lines to stderr and, when configured, a
// durable log file so background daemons can capture stall progress in real time.
type EventLog struct {
	mu sync.Mutex
	f  *os.File
}

// NewEventLogFromSettings opens the log file named by log_file (default
// ~/.squadron/localdev/plugin.log). A leading "~/" is expanded. If the file
// cannot be opened, logging falls back to stderr only.
func NewEventLogFromSettings(settings map[string]string) *EventLog {
	path := settings["log_file"]
	if path == "" {
		path = defaultLogFile()
	}
	if path == "" || path == "-" {
		return &EventLog{}
	}
	path = expandHome(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &EventLog{}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return &EventLog{}
	}
	return &EventLog{f: f}
}

func (l *EventLog) Heartbeat(session string, elapsed time.Duration, bytesIn int64, idle time.Duration) {
	line := fmt.Sprintf("bedrock heartbeat session=%s elapsed=%s bytes_in=%d", session, elapsed.Round(time.Second), bytesIn)
	if idle >= time.Second {
		line += fmt.Sprintf(" idle=%s", idle.Round(time.Second))
	}
	l.write(line)
}

func (l *EventLog) Stalled(session string, idle, threshold time.Duration) {
	l.write(fmt.Sprintf("bedrock stalled session=%s idle=%s (>= stall_detection_seconds=%ds) — cancelling",
		session, idle.Round(time.Second), int(threshold.Seconds())))
}

func (l *EventLog) write(msg string) {
	line := time.Now().UTC().Format(time.RFC3339) + " " + msg + "\n"
	fmt.Fprint(os.Stderr, line)
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_, _ = l.f.WriteString(line)
	}
}

func defaultLogFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".squadron", "localdev", "plugin.log")
}

func expandHome(path string) string {
	if path == "~" || len(path) >= 2 && path[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// --- session label plumbing -------------------------------------------------

type sessionKey struct{}

// ContextWithSession attaches a session id used to label heartbeat/stall log
// lines. Callers (the runner) set it before invoking the agent loop.
func ContextWithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionFromContext returns the session label, or "" if unset.
func SessionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionKey{}).(string); ok {
		return v
	}
	return ""
}
