package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ericlakich/squadron-dev/provider"
)

// TestConverseAccumulator reassembles a realistic ConverseStream sequence: a text
// delta, a tool_use block whose input JSON arrives across two deltas, a message
// stop, and a usage metadata event.
func TestConverseAccumulator(t *testing.T) {
	acc := &converseAccumulator{}
	events := []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberMessageStart{Value: types.MessageStartEvent{Role: types.ConversationRoleAssistant}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &types.ContentBlockDeltaMemberText{Value: "Working"},
		}},
		&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
			ContentBlockIndex: aws.Int32(1),
			Start:             &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{ToolUseId: aws.String("t1"), Name: aws.String("run")}},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(1),
			Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`{"cmd":`)}},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(1),
			Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`"go test"}`)}},
		}},
		&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(1)}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonToolUse}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{InputTokens: aws.Int32(11), OutputTokens: aws.Int32(22)},
		}},
	}
	for _, e := range events {
		if err := acc.add(e); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	resp := acc.result()

	if resp.Text != "Working" {
		t.Errorf("text = %q, want Working", resp.Text)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Errorf("stop = %v, want tool_use", resp.StopReason)
	}
	if len(resp.ToolUses) != 1 {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	tu := resp.ToolUses[0]
	if tu.ID != "t1" || tu.Name != "run" || string(tu.Input) != `{"cmd":"go test"}` {
		t.Errorf("tool use = %+v (input %s)", tu, tu.Input)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 22 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// TestConverseAccumulatorEmptyToolInput defaults missing tool input to {}.
func TestConverseAccumulatorEmptyToolInput(t *testing.T) {
	acc := &converseAccumulator{}
	_ = acc.add(&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
		ContentBlockIndex: aws.Int32(0),
		Start:             &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{ToolUseId: aws.String("t9"), Name: aws.String("noargs")}},
	}})
	_ = acc.add(&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonToolUse}})
	resp := acc.result()
	if len(resp.ToolUses) != 1 || string(resp.ToolUses[0].Input) != "{}" {
		t.Errorf("empty tool input = %+v", resp.ToolUses)
	}
}
