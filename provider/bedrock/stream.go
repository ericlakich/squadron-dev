package bedrock

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ericlakich/squadron-dev/provider"
)

// converseAccumulator reassembles a streamed ConverseStream response into a single
// provider.Response. ConverseStream emits, per indexed content block: a start
// event (text or a tool_use with its id/name), one or more delta events (text
// fragments or partial tool-input JSON), and a stop event; plus a message-stop
// (stop reason) and a metadata (usage) event.
type converseAccumulator struct {
	text   strings.Builder
	blocks map[int32]*blockAcc
	order  []int32
	resp   provider.Response
}

type blockAcc struct {
	isTool bool
	id     string
	name   string
	input  strings.Builder // partial tool-input JSON
}

func (a *converseAccumulator) add(ev types.ConverseStreamOutput) error {
	if a.blocks == nil {
		a.blocks = map[int32]*blockAcc{}
	}
	switch e := ev.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		idx := aws.ToInt32(e.Value.ContentBlockIndex)
		if start, ok := e.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
			a.blocks[idx] = &blockAcc{isTool: true, id: aws.ToString(start.Value.ToolUseId), name: aws.ToString(start.Value.Name)}
			a.order = append(a.order, idx)
		}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		idx := aws.ToInt32(e.Value.ContentBlockIndex)
		switch d := e.Value.Delta.(type) {
		case *types.ContentBlockDeltaMemberText:
			a.text.WriteString(d.Value)
		case *types.ContentBlockDeltaMemberToolUse:
			if b := a.blocks[idx]; b != nil {
				b.input.WriteString(aws.ToString(d.Value.Input))
			}
		}
	case *types.ConverseStreamOutputMemberMessageStop:
		a.resp.StopReason = mapStopReason(e.Value.StopReason)
	case *types.ConverseStreamOutputMemberMetadata:
		if e.Value.Usage != nil {
			a.resp.Usage.InputTokens = int(aws.ToInt32(e.Value.Usage.InputTokens))
			a.resp.Usage.OutputTokens = int(aws.ToInt32(e.Value.Usage.OutputTokens))
		}
	case *types.ConverseStreamOutputMemberMessageStart, *types.ConverseStreamOutputMemberContentBlockStop:
		// nothing to accumulate
	}
	return nil
}

func (a *converseAccumulator) result() *provider.Response {
	a.resp.Text = a.text.String()
	for _, idx := range a.order {
		b := a.blocks[idx]
		if b == nil || !b.isTool {
			continue
		}
		input := strings.TrimSpace(b.input.String())
		if input == "" {
			input = "{}"
		}
		a.resp.ToolUses = append(a.resp.ToolUses, provider.ToolUse{
			ID:    b.id,
			Name:  b.name,
			Input: []byte(input),
		})
	}
	return &a.resp
}

// eventSize is a rough byte count of a stream event, for heartbeat reporting.
func eventSize(ev types.ConverseStreamOutput) int {
	switch e := ev.(type) {
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		switch d := e.Value.Delta.(type) {
		case *types.ContentBlockDeltaMemberText:
			return len(d.Value)
		case *types.ContentBlockDeltaMemberToolUse:
			return len(aws.ToString(d.Value.Input))
		}
	case *types.ConverseStreamOutputMemberContentBlockStart:
		return len(fmt.Sprint(e.Value.Start))
	}
	return 1
}
