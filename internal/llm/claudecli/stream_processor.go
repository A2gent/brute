package claudecli

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

type toolBlockState struct {
	index     int
	id        string
	name      string
	input     strings.Builder
	started   bool
	completed bool
}

type streamProcessor struct {
	onEvent               func(llm.StreamEvent) error
	content               strings.Builder
	usage                 llm.TokenUsage
	stopReason            string
	providerSessionCursor string
	finalResult           cliResult
	sawResult             bool
	assistantContent      string

	blocksByIndex        map[int]*toolBlockState
	toolsByID            map[string]*toolBlockState
	emittedToolOutput    map[string]bool
	emittedToolCompleted map[string]bool
}

func newStreamProcessor(onEvent func(llm.StreamEvent) error) *streamProcessor {
	return &streamProcessor{
		onEvent:              onEvent,
		blocksByIndex:        make(map[int]*toolBlockState),
		toolsByID:            make(map[string]*toolBlockState),
		emittedToolOutput:    make(map[string]bool),
		emittedToolCompleted: make(map[string]bool),
	}
}

func (p *streamProcessor) emit(ev llm.StreamEvent) error {
	if p.onEvent == nil {
		return nil
	}
	return p.onEvent(ev)
}

func (p *streamProcessor) handleEnvelope(event cliStreamEnvelope) error {
	if event.SessionID != "" {
		p.providerSessionCursor = event.SessionID
	}

	switch event.Type {
	case "system":
		return p.handleSystem(event)
	case "stream_event":
		return p.handleStreamEvent(event)
	case "assistant":
		return p.handleAssistant(event)
	case "user":
		return p.handleUserMessage(event.Message)
	case "result":
		return p.handleResult(event)
	default:
		return nil
	}
}

func (p *streamProcessor) handleSystem(event cliStreamEnvelope) error {
	switch event.Subtype {
	case "status":
		status := firstNonEmpty(event.Status, event.Subtype)
		if status == "" {
			return nil
		}
		warning := firstNonEmpty(event.MessageText, status)
		return p.emit(llm.StreamEvent{
			Type:           llm.StreamEventRuntimeWarning,
			RuntimeStatus:  status,
			RuntimeWarning: warning,
		})
	case "compact_boundary":
		return p.emit(llm.StreamEvent{
			Type:           llm.StreamEventRuntimeWarning,
			RuntimeStatus:  "compact_boundary",
			RuntimeWarning: "compact_boundary",
		})
	default:
		return nil
	}
}

func (p *streamProcessor) handleStreamEvent(event cliStreamEnvelope) error {
	stream := event.Event
	if stream.Message.Usage != nil {
		p.usage = mergeUsage(p.usage, usageFromRaw(stream.Message.Usage))
	}
	if stream.Usage != nil {
		p.usage = mergeUsage(p.usage, usageFromRaw(stream.Usage))
	}
	if stream.Delta.StopReason != "" {
		p.stopReason = stream.Delta.StopReason
	}
	if stream.Message.StopReason != "" {
		p.stopReason = stream.Message.StopReason
	}

	switch stream.Type {
	case "message_start":
		return nil
	case "content_block_start":
		return p.handleContentBlockStart(stream)
	case "content_block_delta":
		return p.handleContentBlockDelta(stream)
	case "content_block_stop":
		return p.handleContentBlockStop(stream)
	case "message_delta", "message_stop":
		return nil
	default:
		return nil
	}
}

func (p *streamProcessor) handleContentBlockStart(stream cliStreamEvent) error {
	block := stream.ContentBlock
	switch block.Type {
	case "tool_use":
		state := &toolBlockState{
			index:   stream.Index,
			id:      strings.TrimSpace(block.ID),
			name:    strings.TrimSpace(block.Name),
			started: true,
		}
		if input := toolInputString(block.Input); input != "" {
			state.input.WriteString(input)
		}
		p.blocksByIndex[stream.Index] = state
		if state.id != "" {
			p.toolsByID[state.id] = state
		}
		return p.emit(llm.StreamEvent{
			Type:          llm.StreamEventToolStarted,
			ToolCallIndex: stream.Index,
			ToolCallID:    state.id,
			ToolCallName:  state.name,
		})
	default:
		return nil
	}
}

func (p *streamProcessor) handleContentBlockDelta(stream cliStreamEvent) error {
	delta := stream.Delta
	switch delta.Type {
	case "thinking_delta":
		if delta.Thinking == "" {
			return nil
		}
		return p.emit(llm.StreamEvent{
			Type:           llm.StreamEventReasoningDelta,
			ReasoningDelta: delta.Thinking,
		})
	case "text_delta":
		if delta.Text == "" {
			return nil
		}
		p.content.WriteString(delta.Text)
		return p.emit(llm.StreamEvent{
			Type:         llm.StreamEventContentDelta,
			ContentDelta: delta.Text,
		})
	case "input_json_delta":
		state := p.blocksByIndex[stream.Index]
		if state == nil {
			return nil
		}
		state.input.WriteString(delta.PartialJSON)
		return p.emit(llm.StreamEvent{
			Type:           llm.StreamEventToolUpdated,
			ToolCallIndex:  state.index,
			ToolCallID:     state.id,
			ToolCallName:   state.name,
			ToolInputDelta: state.input.String(),
		})
	default:
		return nil
	}
}

func (p *streamProcessor) handleContentBlockStop(stream cliStreamEvent) error {
	state := p.blocksByIndex[stream.Index]
	if state == nil || state.completed || state.id == "" {
		return nil
	}
	state.completed = true
	p.emittedToolCompleted[state.id] = true
	return p.emit(llm.StreamEvent{
		Type:           llm.StreamEventToolCompleted,
		ToolCallIndex:  state.index,
		ToolCallID:     state.id,
		ToolCallName:   state.name,
		ToolInputDelta: state.input.String(),
	})
}

func (p *streamProcessor) handleAssistant(event cliStreamEnvelope) error {
	message := event.Message
	if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
		return p.handleUserMessage(message)
	}

	p.assistantContent = streamMessageText(message)
	if message.Usage != nil {
		p.usage = mergeUsage(p.usage, usageFromRaw(message.Usage))
	}
	if message.StopReason != "" {
		p.stopReason = message.StopReason
	}

	for _, item := range message.Content {
		switch item.Type {
		case "tool_use":
			if err := p.fallbackToolUse(item, -1); err != nil {
				return err
			}
		case "tool_result":
			if err := p.emitToolOutput(item.ToolUseID, "", item.Content, item.IsError); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *streamProcessor) handleUserMessage(message cliStreamMessage) error {
	for _, item := range message.Content {
		if item.Type != "tool_result" {
			continue
		}
		if err := p.emitToolOutput(item.ToolUseID, "", item.Content, item.IsError); err != nil {
			return err
		}
	}
	return nil
}

func (p *streamProcessor) fallbackToolUse(item cliStreamContent, index int) error {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return nil
	}
	if p.emittedToolCompleted[id] {
		return nil
	}
	name := strings.TrimSpace(item.Name)
	input := toolInputString(item.Input)
	state, ok := p.toolsByID[id]
	if !ok {
		state = &toolBlockState{id: id, name: name, index: index}
		p.toolsByID[id] = state
	}
	if name != "" {
		state.name = name
	}
	if input != "" {
		state.input.Reset()
		state.input.WriteString(input)
	}
	if !state.started {
		state.started = true
		if err := p.emit(llm.StreamEvent{
			Type:          llm.StreamEventToolStarted,
			ToolCallIndex: state.index,
			ToolCallID:    state.id,
			ToolCallName:  state.name,
		}); err != nil {
			return err
		}
	}
	if input != "" {
		if err := p.emit(llm.StreamEvent{
			Type:           llm.StreamEventToolUpdated,
			ToolCallIndex:  state.index,
			ToolCallID:     state.id,
			ToolCallName:   state.name,
			ToolInputDelta: input,
		}); err != nil {
			return err
		}
	}
	if !state.completed {
		state.completed = true
		p.emittedToolCompleted[id] = true
		if err := p.emit(llm.StreamEvent{
			Type:           llm.StreamEventToolCompleted,
			ToolCallIndex:  state.index,
			ToolCallID:     state.id,
			ToolCallName:   state.name,
			ToolInputDelta: input,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *streamProcessor) emitToolOutput(toolUseID, fallbackName string, contentRaw json.RawMessage, isError bool) error {
	id := strings.TrimSpace(toolUseID)
	if id == "" || p.emittedToolOutput[id] {
		return nil
	}
	content, invalid := toolResultContent(contentRaw)
	if invalid {
		return nil
	}
	name := fallbackName
	if state, ok := p.toolsByID[id]; ok && state.name != "" {
		name = state.name
	}
	p.emittedToolOutput[id] = true
	return p.emit(llm.StreamEvent{
		Type:         llm.StreamEventToolOutput,
		ToolCallID:   id,
		ToolCallName: name,
		ToolOutput:   content,
		ToolIsError:  isError,
	})
}

func (p *streamProcessor) handleResult(event cliStreamEnvelope) error {
	p.sawResult = true
	p.finalResult = cliResult{
		Type:          event.Type,
		Subtype:       event.Subtype,
		IsError:       event.IsError,
		Result:        event.Result,
		Error:         event.Error,
		SessionID:     event.SessionID,
		StopReason:    event.StopReason,
		TotalCostUSD:  event.TotalCostUSD,
		DurationMS:    event.DurationMS,
		DurationAPIMS: event.DurationAPIMS,
		NumTurns:      event.NumTurns,
		Usage:         event.Usage,
	}
	if event.Usage != nil {
		p.usage = mergeUsage(p.usage, usageFromRaw(event.Usage))
	}
	if event.StopReason != "" {
		p.stopReason = event.StopReason
	}
	if event.SessionID != "" {
		p.providerSessionCursor = event.SessionID
	}
	if event.TotalCostUSD != 0 || event.DurationMS != 0 || event.DurationAPIMS != 0 || event.NumTurns != 0 {
		if err := p.emit(llm.StreamEvent{
			Type:          llm.StreamEventCost,
			TotalCostUSD:  event.TotalCostUSD,
			DurationMS:    event.DurationMS,
			DurationAPIMS: event.DurationAPIMS,
			NumTurns:      event.NumTurns,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *streamProcessor) finalize(onEvent func(llm.StreamEvent) error) error {
	if onEvent != nil {
		if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: p.usage}); err != nil {
			return err
		}
	}
	return nil
}
