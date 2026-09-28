// Package aitest provides a scripted ai.Provider for tests.
package aitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// Block is one scripted content block: text or a tool call.
type Block struct {
	Text      string
	ToolName  string
	ToolID    string
	Arguments map[string]any
}

// Text is a text block.
func Text(text string) Block { return Block{Text: text} }

// ToolCall is a tool call block. An empty id gets a unique one.
func ToolCall(name string, args map[string]any, id string) Block {
	if id == "" {
		id = fmt.Sprintf("tool:%d", toolIDs.Add(1))
	}
	return Block{ToolName: name, ToolID: id, Arguments: args}
}

var toolIDs atomic.Int64

// Response is one scripted assistant turn. An empty StopReason is "stop";
// "error" fails the stream with ErrorMessage.
type Response struct {
	Content      []Block
	StopReason   ai.StopReason
	ErrorMessage string
}

// FauxProvider answers each request with the next scripted Response,
// streaming text in 4-byte deltas.
type FauxProvider struct {
	providerID string
	model      string
	mu         sync.Mutex
	responses  []Response
}

// NewFauxProvider returns a scripted provider; empty ids default to
// "faux"/"faux-1".
func NewFauxProvider(providerID, model string) *FauxProvider {
	if providerID == "" {
		providerID = "faux"
	}
	if model == "" {
		model = "faux-1"
	}
	return &FauxProvider{providerID: providerID, model: model}
}

// ID returns the provider id.
func (p *FauxProvider) ID() string { return p.providerID }

// Close is a no-op.
func (p *FauxProvider) Close() error { return nil }

// SetResponses replaces the scripted responses.
func (p *FauxProvider) SetResponses(responses ...Response) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append([]Response(nil), responses...)
}

// Stream emits the next scripted response, or an error when none is left.
func (p *FauxProvider) Stream(ctx context.Context, _ ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.mu.Lock()
	var response *Response
	if len(p.responses) > 0 {
		response = &p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()

	stream := ai.NewAssistantMessageEventStream()
	go func() {
		msg := &ai.AssistantMessage{
			Content: []ai.AssistantContentBlock{}, API: "faux", Provider: p.providerID, Model: p.model,
			StopReason: ai.StopReasonPending, Timestamp: time.Now().UnixMilli(),
		}
		push := func(event ai.AssistantMessageEvent) { _ = stream.Push(event) }
		fail := func(reason ai.StopReason, err error) {
			msg.StopReason, msg.ErrorMessage = reason, err.Error()
			push(ai.ErrorEvent{Reason: reason, Error: msg})
		}
		push(ai.StartEvent{Partial: msg})
		if response == nil {
			fail(ai.StopReasonError, errors.New("no more faux responses queued"))
			return
		}
		for _, block := range response.Content {
			if err := ctx.Err(); err != nil {
				fail(ai.StopReasonAborted, err)
				return
			}
			index := len(msg.Content)
			if block.ToolName == "" {
				msg.Content = append(msg.Content, ai.TextContent{})
				push(ai.TextStartEvent{ContentIndex: index, Partial: msg})
				for start := 0; start < len(block.Text); start += 4 {
					chunk := block.Text[start:min(start+4, len(block.Text))]
					msg.Content[index] = ai.TextContent{Text: block.Text[:start+len(chunk)]}
					push(ai.TextDeltaEvent{ContentIndex: index, Delta: chunk, Partial: msg})
				}
				push(ai.TextEndEvent{ContentIndex: index, Content: block.Text, Partial: msg})
				continue
			}
			call := ai.ToolCall{ID: block.ToolID, Name: block.ToolName, Arguments: ai.JsonObject{}}
			msg.Content = append(msg.Content, call)
			push(ai.ToolCallStartEvent{ContentIndex: index, Partial: msg})
			arguments, _ := json.Marshal(block.Arguments)
			call.Arguments = ai.JsonObject(block.Arguments)
			if call.Arguments == nil {
				call.Arguments = ai.JsonObject{}
			}
			msg.Content[index] = call
			push(ai.ToolCallDeltaEvent{ContentIndex: index, Delta: string(arguments), Partial: msg})
			push(ai.ToolCallEndEvent{ContentIndex: index, ToolCall: call, Partial: msg})
		}
		reason := response.StopReason
		if reason == "" {
			reason = ai.StopReasonStop
		}
		if reason == ai.StopReasonError {
			fail(ai.StopReasonError, errors.New(response.ErrorMessage))
			return
		}
		msg.StopReason, msg.ErrorMessage = reason, response.ErrorMessage
		push(ai.DoneEvent{Reason: reason, Message: msg})
	}()
	return stream, nil
}
