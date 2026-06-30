// Package provider defines the pluggable LLM backend ("the brain") that powers
// local development. A provider runs a single inference turn given a conversation
// and a set of tool specifications; the agent loop in package agent is responsible
// for executing any tools the model requests and feeding the results back.
//
// Providers are registered by name so the plugin can select one from HCL config.
// AWS Bedrock is the first (and currently only) provider; adding another (OpenAI,
// Anthropic direct, Ollama, ...) is a matter of implementing Provider and calling
// Register from the new package's init function.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Role identifies who authored a message in the conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// StopReason is the normalized reason a provider stopped generating.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopOther     StopReason = "other"
)

// ToolUse is a request from the model to invoke a tool.
type ToolUse struct {
	ID    string          // provider-assigned id, echoed back in the tool result
	Name  string          // tool name
	Input json.RawMessage // tool arguments as a JSON object
}

// ToolResult is the outcome of executing a tool, fed back to the model on the
// next turn.
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Block is a single content block within a message. Exactly one field is set.
type Block struct {
	Text       string
	ToolUse    *ToolUse
	ToolResult *ToolResult
}

// TextBlock is a convenience constructor for a plain text block.
func TextBlock(s string) Block { return Block{Text: s} }

// Message is one turn in the conversation.
type Message struct {
	Role   Role
	Blocks []Block
}

// ToolSpec describes a tool the model may call. InputSchema is a JSON Schema
// object describing the tool's parameters.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Request is a single inference turn.
type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Temperature float32
}

// Usage reports token consumption for a turn.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is the model's reply for one turn.
type Response struct {
	StopReason StopReason
	Text       string
	ToolUses   []ToolUse
	Usage      Usage
}

// Provider is the pluggable LLM backend that drives local development.
type Provider interface {
	// Name returns the registered provider name (e.g. "bedrock").
	Name() string
	// Converse runs one inference turn.
	Converse(ctx context.Context, req *Request) (*Response, error)
}

// Factory builds a Provider from plugin settings. Implementations must resolve
// credentials from the ambient environment (AWS credential chain, env vars,
// IAM roles) rather than from settings.
type Factory func(settings map[string]string) (Provider, error)

var registry = map[string]Factory{}

// Register makes a provider available by name. Call from a provider package's
// init function.
func Register(name string, f Factory) {
	registry[name] = f
}

// New constructs a registered provider by name.
func New(name string, settings map[string]string) (Provider, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (available: %s)", name, strings.Join(Available(), ", "))
	}
	return f(settings)
}

// Available returns the sorted names of all registered providers.
func Available() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
