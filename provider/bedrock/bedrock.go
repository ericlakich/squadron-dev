// Package bedrock implements the provider.Provider interface on top of the
// Amazon Bedrock Converse API, served by the bedrock-runtime endpoint. Converse
// exposes a unified, tool-use-capable message format across the foundation models
// Bedrock hosts (Anthropic Claude, etc.). It registers as "bedrock-runtime" (with
// "bedrock" kept as a backward-compatible alias); the sibling mantle package
// provides the "bedrock-mantle" (Responses API) provider.
//
// Authentication supports two methods:
//
//   - Bedrock API key (the "API connection method"): set the bedrock_api_key
//     setting — typically wired to a Squadron secret. The client then
//     authenticates to the Bedrock Runtime API with an HTTP bearer token instead
//     of SigV4.
//   - AWS credential chain: if no bedrock_api_key is given, credentials resolve
//     the usual way (environment, shared config/profile, SSO, IAM role, or the
//     AWS_BEARER_TOKEN_BEDROCK environment variable).
//
// Either way, settings only select the region, model, and inference parameters.
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"

	"github.com/ericlakich/squadron-dev/provider"
)

const (
	// defaultModelID is a cross-region inference profile for Claude Sonnet on
	// Bedrock. Override with the model_id setting.
	defaultModelID     = "us.anthropic.claude-sonnet-4-20250514-v1:0"
	defaultRegion      = "us-east-1"
	defaultMaxTokens   = 8192
	defaultTemperature = float32(0)
	defaultStreamMode  = "stream"
)

func init() {
	// The AWS Bedrock Converse API is served by the bedrock-runtime endpoint.
	// "bedrock" is kept as a backward-compatible alias for earlier configs.
	provider.Register("bedrock-runtime", New)
	provider.Register("bedrock", New)
}

// Bedrock is a provider backed by the Amazon Bedrock Converse API.
type Bedrock struct {
	client         *bedrockruntime.Client
	modelID        string
	maxTokens      int32
	temperature    float32
	requestTimeout time.Duration
	guard          *provider.Guard
	streamMode     string
}

// New builds a Bedrock provider from settings.
//
// Recognized settings:
//   - bedrock_api_key: Bedrock API key for bearer-token auth (usually a Squadron
//     secret). When set, the client uses the "API connection method" instead of
//     SigV4. Optional.
//   - aws_region:  AWS region (default us-east-1)
//   - aws_profile: shared-config profile name (optional)
//   - model_id:    Bedrock model id or inference profile id
//   - max_tokens:  max output tokens per turn (default 8192)
//   - temperature: sampling temperature (default 0)
//   - request_timeout_seconds: hard ceiling on one model call (default 300)
//   - stall_detection_seconds: cancel if the stream is idle this long (default 90, 0 disables)
//   - log_file: durable path for heartbeat/stall logs (default ~/.squadron/localdev/plugin.log)
//   - stream_mode: "stream" (default) or "non-streaming". Use "non-streaming" for
//     models that don't support ConverseStream properly (e.g. some Qwen configurations).
func New(settings map[string]string) (provider.Provider, error) {
	region := get(settings, "aws_region", defaultRegion)

	requestTimeout, err := provider.ParseRequestTimeout(settings)
	if err != nil {
		return nil, err
	}
	stall, err := provider.ParseStallTimeout(settings)
	if err != nil {
		return nil, err
	}
	guard := provider.NewGuard(stall, provider.NewEventLogFromSettings(settings))

	// A hardened HTTP client makes a dead or half-open Bedrock connection fail
	// fast (keep-alive probes + bounded setup timeouts) rather than blocking a
	// read forever; the per-request context deadline in Converse is the overall
	// bound.
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(provider.HardenedHTTPClient()),
	}
	if profile := settings["aws_profile"]; profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	maxTokens := defaultMaxTokens
	if v := settings["max_tokens"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid max_tokens %q: must be a positive integer", v)
		}
		maxTokens = n
	}

	temp := defaultTemperature
	if v := settings["temperature"]; v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid temperature %q: %w", v, err)
		}
		temp = float32(f)
	}

	streamMode := get(settings, "stream_mode", defaultStreamMode)
	if streamMode != "stream" && streamMode != "non-streaming" {
		return nil, fmt.Errorf("invalid stream_mode %q: must be %q or %q", streamMode, defaultStreamMode, "non-streaming")
	}

	return &Bedrock{
		client:         bedrockruntime.NewFromConfig(cfg, bearerOptions(settings["bedrock_api_key"])...),
		modelID:        get(settings, "model_id", defaultModelID),
		maxTokens:      int32(maxTokens),
		temperature:    temp,
		requestTimeout: requestTimeout,
		guard:          guard,
		streamMode:     streamMode,
	}, nil
}

// bearerOptions returns client options that authenticate to the Bedrock Runtime
// API with an HTTP bearer token (a Bedrock API key). When key is empty it returns
// nil, leaving the client to use the default AWS credential chain.
func bearerOptions(key string) []func(*bedrockruntime.Options) {
	if key == "" {
		return nil
	}
	return []func(*bedrockruntime.Options){
		func(o *bedrockruntime.Options) {
			o.BearerAuthTokenProvider = bearer.TokenProviderFunc(func(context.Context) (bearer.Token, error) {
				return bearer.Token{Value: key}, nil
			})
			o.AuthSchemePreference = []string{"httpBearerAuth"}
		},
	}
}

// Name implements provider.Provider.
func (b *Bedrock) Name() string { return "bedrock-runtime" }

// Converse implements provider.Provider using the Bedrock Converse API.
// It supports both streaming (ConverseStream) and non-streaming (Converse) modes.
// Streaming lets a per-event stall watchdog cancel a genuinely dead connection
// (no events for stall_detection_seconds) without penalizing a legitimately long
// generation, whose tokens keep the stream active. Non-streaming mode is available
// for models that don't support ConverseStream properly.
func (b *Bedrock) Converse(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	maxTokens := b.maxTokens
	if req.MaxTokens > 0 {
		maxTokens = int32(req.MaxTokens)
	}
	temp := b.temperature
	if req.Temperature > 0 {
		temp = req.Temperature
	}

	in := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(b.modelID),
		Messages: toBedrockMessages(req.Messages),
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(maxTokens),
			Temperature: aws.Float32(temp),
		},
	}
	if req.System != "" {
		in.System = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: req.System},
		}
	}
	if len(req.Tools) > 0 {
		tc, err := toToolConfig(req.Tools)
		if err != nil {
			return nil, err
		}
		in.ToolConfig = tc
	}

	// request_timeout_seconds bounds the whole call; the stall guard cancels sooner
	// if the stream goes idle (a dead peer) while leaving active generations alone.
	callCtx, cancel := provider.WithRequestTimeout(ctx, b.requestTimeout)
	defer cancel()
	gctx, gc := b.guard.Start(callCtx, provider.SessionFromContext(ctx))
	defer gc.Finish()

	// Use non-streaming mode for models that don't support ConverseStream properly
	if b.streamMode == "non-streaming" {
		return b.converseNonStreaming(gctx, in, gc, callCtx, ctx)
	}

	// Streaming mode with stall detection
	return b.converseStreaming(gctx, in, gc, callCtx, ctx)
}

// converseStreaming uses ConverseStream for streaming model responses with stall detection.
func (b *Bedrock) converseStreaming(ctx context.Context, in *bedrockruntime.ConverseStreamInput, gc *provider.Call, callCtx, ctxOuter context.Context) (*provider.Response, error) {
	out, err := b.client.ConverseStream(ctx, in)
	if err != nil {
		return nil, b.streamError(err, gc, callCtx, ctxOuter)
	}

	acc := &converseAccumulator{}
	stream := out.GetStream()
	defer stream.Close()
	for event := range stream.Events() {
		gc.Touch(eventSize(event))
		if err := acc.add(event); err != nil {
			return nil, fmt.Errorf("bedrock converse (model %s): %w", b.modelID, err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, b.streamError(err, gc, callCtx, ctxOuter)
	}
	return acc.result(), nil
}

// streamError classifies a ConverseStream failure as a stall, a request-timeout,
// or a generic error.
func (b *Bedrock) streamError(err error, gc *provider.Call, callCtx, ctx context.Context) error {
	if gc.Stalled() {
		return fmt.Errorf("bedrock converse (model %s): %w", b.modelID, gc.Err())
	}
	if callCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return fmt.Errorf("bedrock converse (model %s): timed out after %s (raise request_timeout_seconds if the model needs longer)", b.modelID, b.requestTimeout)
	}
	return fmt.Errorf("bedrock converse (model %s): %w", b.modelID, err)
}

func toBedrockMessages(msgs []provider.Message) []types.Message {
	result := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		role := types.ConversationRoleUser
		if m.Role == provider.RoleAssistant {
			role = types.ConversationRoleAssistant
		}
		content := make([]types.ContentBlock, 0, len(m.Blocks))
		for _, blk := range m.Blocks {
			switch {
			case blk.ToolUse != nil:
				content = append(content, &types.ContentBlockMemberToolUse{
					Value: types.ToolUseBlock{
						ToolUseId: aws.String(blk.ToolUse.ID),
						Name:      aws.String(blk.ToolUse.Name),
						Input:     document.NewLazyDocument(rawToAny(blk.ToolUse.Input)),
					},
				})
			case blk.ToolResult != nil:
				text := blk.ToolResult.Content
				if text == "" {
					text = "(no output)"
				}
				tr := types.ToolResultBlock{
					ToolUseId: aws.String(blk.ToolResult.ToolUseID),
					Content: []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: text},
					},
				}
				if blk.ToolResult.IsError {
					tr.Status = types.ToolResultStatusError
				}
				content = append(content, &types.ContentBlockMemberToolResult{Value: tr})
			default:
				content = append(content, &types.ContentBlockMemberText{Value: blk.Text})
			}
		}
		result = append(result, types.Message{Role: role, Content: content})
	}
	return result
}

func toToolConfig(specs []provider.ToolSpec) (*types.ToolConfiguration, error) {
	tools := make([]types.Tool, 0, len(specs))
	for _, s := range specs {
		var schema any
		if len(s.InputSchema) > 0 {
			if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("tool %q input schema: %w", s.Name, err)
			}
		} else {
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(s.Name),
				Description: aws.String(s.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{
					Value: document.NewLazyDocument(schema),
				},
			},
		})
	}
	return &types.ToolConfiguration{Tools: tools}, nil
}

func documentToRaw(d document.Interface) (json.RawMessage, error) {
	if d == nil {
		return json.RawMessage("{}"), nil
	}
	// MarshalSmithyDocument returns the document's JSON bytes directly and works
	// for both the documents we build (NewLazyDocument) and those the SDK returns
	// when deserializing a response, avoiding the typed-target restrictions of
	// UnmarshalSmithyDocument.
	b, err := d.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return json.RawMessage("{}"), nil
	}
	return json.RawMessage(b), nil
}

func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{}
	}
	return v
}

func mapStopReason(r types.StopReason) provider.StopReason {
	switch r {
	case types.StopReasonEndTurn, types.StopReasonStopSequence:
		return provider.StopEndTurn
	case types.StopReasonToolUse:
		return provider.StopToolUse
	case types.StopReasonMaxTokens:
		return provider.StopMaxTokens
	default:
		return provider.StopOther
	}
}

func get(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return def
}

// converseNonStreaming uses the non-streaming Converse API instead of ConverseStream.
// This is a workaround for models that don't support streaming properly (e.g. some
// Qwen configurations). It still uses the guard for request timeout, but doesn't
// benefit from per-event stall detection.
func (b *Bedrock) converseNonStreaming(ctx context.Context, in *bedrockruntime.ConverseStreamInput, gc *provider.Call, callCtx, ctxOuter context.Context) (*provider.Response, error) {
	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:         in.ModelId,
		Messages:        in.Messages,
		InferenceConfig: in.InferenceConfig,
		System:          in.System,
		ToolConfig:      in.ToolConfig,
	})
	if err != nil {
		return nil, b.streamError(err, gc, callCtx, ctxOuter)
	}

	acc := &converseAccumulator{}

	// Extract text content and tool uses from the message
	if msg, ok := out.Output.(*types.ConverseOutputMemberMessage); ok {
		for _, block := range msg.Value.Content {
			if txt, ok := block.(*types.ContentBlockMemberText); ok {
				acc.text.WriteString(txt.Value)
			}
			if tu, ok := block.(*types.ContentBlockMemberToolUse); ok {
				raw, err := documentToRaw(tu.Value.Input)
				if err != nil {
					raw = json.RawMessage("{}")
				}
				acc.resp.ToolUses = append(acc.resp.ToolUses, provider.ToolUse{
					ID:    aws.ToString(tu.Value.ToolUseId),
					Name:  aws.ToString(tu.Value.Name),
					Input: raw,
				})
			}
		}
	}

	// Extract usage and stop reason
	acc.resp.Usage.InputTokens = int(aws.ToInt32(out.Usage.InputTokens))
	acc.resp.Usage.OutputTokens = int(aws.ToInt32(out.Usage.OutputTokens))
	acc.resp.StopReason = mapStopReason(out.StopReason)

	return acc.result(), nil
}
