package mantle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ericlakich/squadron-dev/provider"
)

func TestProviderRegistered(t *testing.T) {
	found := false
	for _, n := range provider.Available() {
		if n == "bedrock-mantle" {
			found = true
		}
	}
	if !found {
		t.Fatal("bedrock-mantle provider was not registered")
	}
}

func TestEndpointURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://bedrock-mantle.us-east-1.api.aws", responsesPath, "https://bedrock-mantle.us-east-1.api.aws/v1/responses"},
		{"https://bedrock-mantle.us-east-1.api.aws/", responsesPath, "https://bedrock-mantle.us-east-1.api.aws/v1/responses"},
		{"https://bedrock-mantle.us-east-1.api.aws/v1/responses", responsesPath, "https://bedrock-mantle.us-east-1.api.aws/v1/responses"},
		{"https://bedrock-mantle.us-east-1.api.aws", chatPath, "https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
		{"https://gw.example.com/proxy", chatPath, "https://gw.example.com/proxy/v1/chat/completions"},
	}
	for _, c := range cases {
		if got := endpointURL(c.base, c.path); got != c.want {
			t.Errorf("endpointURL(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("BEDROCK_API_KEY", "")
	if _, err := New(map[string]string{"aws_region": "us-east-1"}); err == nil {
		t.Error("expected error when no API key is provided")
	}
}

func TestNewRejectsUnknownAPI(t *testing.T) {
	if _, err := New(map[string]string{"bedrock_api_key": "k", "mantle_api": "bogus"}); err == nil {
		t.Error("expected error for unknown mantle_api")
	}
}

func TestNewSelectsEndpointByAPI(t *testing.T) {
	responses, err := New(map[string]string{"aws_region": "us-west-2", "bedrock_api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got := responses.(*Mantle).endpoint; got != "https://bedrock-mantle.us-west-2.api.aws/v1/responses" {
		t.Errorf("responses endpoint = %q", got)
	}

	chat, err := New(map[string]string{"aws_region": "us-west-2", "bedrock_api_key": "k", "mantle_api": "chat_completions"})
	if err != nil {
		t.Fatal(err)
	}
	m := chat.(*Mantle)
	if m.api != apiChatCompletions {
		t.Errorf("api = %q, want chat_completions", m.api)
	}
	if m.endpoint != "https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions" {
		t.Errorf("chat endpoint = %q", m.endpoint)
	}

	// "chat" is accepted as an alias for chat_completions.
	alias, err := New(map[string]string{"bedrock_api_key": "k", "mantle_api": "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if alias.(*Mantle).api != apiChatCompletions {
		t.Error("mantle_api=chat should alias to chat_completions")
	}
}

func TestNewFallsBackToEnv(t *testing.T) {
	t.Setenv("BEDROCK_MANTLE_ENDPOINT", "https://env.example.com")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "env-token")
	p, err := New(map[string]string{})
	if err != nil {
		t.Fatalf("New with env fallbacks: %v", err)
	}
	m := p.(*Mantle)
	if m.endpoint != "https://env.example.com/v1/responses" {
		t.Errorf("endpoint = %q", m.endpoint)
	}
	if m.apiKey != "env-token" {
		t.Errorf("apiKey = %q, want env-token", m.apiKey)
	}
}

func TestDescribeError(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"bad model"}}`)
	if got := describeError(400, body); got != "HTTP 400: bad model" {
		t.Errorf("describeError = %q", got)
	}
	if got := describeError(500, []byte("upstream boom")); got != "HTTP 500: upstream boom" {
		t.Errorf("describeError fallback = %q", got)
	}
}

func TestNewParsesStallTimeout(t *testing.T) {
	p, err := New(map[string]string{"bedrock_api_key": "k", "stall_detection_seconds": "10"})
	if err != nil {
		t.Fatal(err)
	}
	_ = p // stall is held on the guard; parsing success is the assertion
	if _, err := New(map[string]string{"bedrock_api_key": "k", "stall_detection_seconds": "-1"}); err == nil {
		t.Error("expected error for negative stall_detection_seconds")
	}
	if _, err := New(map[string]string{"bedrock_api_key": "k", "stall_detection_seconds": "0"}); err != nil {
		t.Errorf("0 should be valid (disables stall): %v", err)
	}
}

// TestConverseStallsOnIdleStream proves a stream that goes idle mid-flight is
// cancelled with ErrStalled well before the (much larger) request timeout.
func TestConverseStallsOnIdleStream(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"type":"response.created","response":{"status":"in_progress"}}`)
		<-release // stream then goes silent without a terminal event
	}))
	defer srv.Close()
	defer close(release)

	p, err := New(map[string]string{
		"mantle_endpoint":         srv.URL,
		"bedrock_api_key":         "k",
		"stall_detection_seconds": "1",
		"request_timeout_seconds": "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err == nil || !errors.Is(err, provider.ErrStalled) {
		t.Fatalf("expected ErrStalled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("stall detection too slow: %s (should be near 1s, not the 30s request timeout)", elapsed)
	}
}

func TestNewParsesRequestTimeout(t *testing.T) {
	p, err := New(map[string]string{"bedrock_api_key": "k", "request_timeout_seconds": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Mantle).requestTimeout; got != 42*time.Second {
		t.Errorf("requestTimeout = %s, want 42s", got)
	}
	if _, err := New(map[string]string{"bedrock_api_key": "k", "request_timeout_seconds": "0"}); err == nil {
		t.Error("expected error for request_timeout_seconds=0")
	}
}

// TestConverseTimesOutOnHungServer proves a stalled endpoint fails with a clear
// timeout error rather than blocking indefinitely.
func TestConverseTimesOutOnHungServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test releases it, well past the request timeout
	}))
	defer srv.Close()
	defer close(release)

	p, err := New(map[string]string{
		"mantle_endpoint":         srv.URL,
		"bedrock_api_key":         "k",
		"request_timeout_seconds": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err == nil {
		t.Fatal("expected a timeout error from the hung server")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to mention a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Converse took %s; the request timeout did not fire promptly", elapsed)
	}
}

func TestRawArgs(t *testing.T) {
	if got := string(rawArgs(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("rawArgs valid = %q", got)
	}
	if got := string(rawArgs("")); got != `{}` {
		t.Errorf("rawArgs empty = %q, want {}", got)
	}
	if got := string(rawArgs("not json")); got != `{}` {
		t.Errorf("rawArgs invalid = %q, want {}", got)
	}
}
