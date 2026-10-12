package chatcompletions

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dillonzq/api-translator/internal/adapter/shared"
	"github.com/dillonzq/api-translator/internal/errclass"
)

// StreamConverter incrementally converts an upstream Chat Completions
// SSE stream into the client protocol's stream (FR-006, AC §B): "chat-completions"
// frames pass through verbatim, "messages" synthesizes Anthropic Messages
// events, "responses" synthesizes Responses events. Partial SSE
// lines are buffered across Feed calls; tool-call argument fragments are
// accumulated per tool_calls index until the stream terminates.
//
// A converter is safe for SEQUENTIAL Feed calls only — it is not
// goroutine-safe; use one converter per upstream stream. Synthesis
// follows choices[0] (upstream serves n=1 routes).
type StreamConverter struct {
	sourceFormat string
	lineBuf      []byte // partial SSE line carried across Feed calls
	done         bool
	ending       bool

	started          bool // message_start / first chunk seen
	id               string
	model            string
	claudeEm         shared.ClaudeEventEmitter // canonical Messages frames bound to first-chunk identity
	textOpen         bool                      // claude text content_block open
	textIndex        int                       // claude index of the currently-open text block
	nextIndex        int                       // next output/block index
	msgIndex         int                       // announced assistant message item index (-1 until text)
	tools            map[int64]*streamTool
	toolOrder        []int64 // upstream tool_call indices in first-arrival order
	toolsSeen        bool    // any tool_calls entry observed (terminal-reason precedence)
	usage            *ccUsage
	finished         bool   // finish_reason processed
	heldFinish       string // finish_reason awaiting terminal emission (flushed on the next data line or [DONE], so the standard include_usage trailer lands in the terminal event; Flush covers close-without-[DONE])
	terminalSent     bool   // claudeTerminal already emitted message_delta (Flush must still close with message_stop)
	flushed          bool   // Flush already ran (one-shot guard)
	reasonOpen       bool
	reasonIndex      int
	respReason       strings.Builder
	refused          bool
	refusalFirst     bool
	respRefusal      strings.Builder
	respText         strings.Builder // Responses accumulated output_text
	pending          []messagesPending
	respTools        *shared.ResponseTools
	citationText     strings.Builder
	citations        []shared.Citation
	citationSent     int
	citationKeys     map[string]bool
	messagesText     strings.Builder
	messagesTextBase int
	messagesTextEnd  int
}

// streamTool accumulates one upstream tool_calls index; args collects
// argument fragments so the terminal response.completed output carries the
// complete call (FR-006).
type streamTool struct {
	blockIndex    int
	id            string
	name          string
	args          strings.Builder
	stopped       bool // content_block_stop emitted
	announced     bool
	sent          int
	customEmitted int // custom_tool_call_input bytes already emitted as deltas
}

// NewStreamConverter returns a converter translating Chat Completions
// SSE into sourceFormat's stream shape.
func NewStreamConverter(sourceFormat string, tools ...*shared.ResponseTools) *StreamConverter {
	sc := &StreamConverter{
		sourceFormat: sourceFormat,
		msgIndex:     -1,
		reasonIndex:  -1,
		tools:        map[int64]*streamTool{},
	}
	if len(tools) > 0 && tools[0] != nil {
		sc.respTools = tools[0]
	}
	return sc
}

// Feed consumes one upstream chunk (any split of the byte stream),
// returning the client-protocol events completed by this chunk, whether
// the stream is done ([DONE] seen), and a classified error for
// malformed chunks (FR-006). Errors carry at most an 80-character
// redacted snippet — never the full upstream body.
func (sc *StreamConverter) Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error) {
	if sc.done {
		return nil, true, nil
	}
	sc.lineBuf = append(sc.lineBuf, chunk...)
	for {
		i := bytes.IndexByte(sc.lineBuf, '\n')
		if i < 0 {
			break // keep trailing partial line buffered
		}
		line := strings.TrimSuffix(string(sc.lineBuf[:i]), "\r")
		sc.lineBuf = sc.lineBuf[i+1:]
		ev, eErr := sc.handleLine(line)
		if eErr != nil {
			return events, false, eErr
		}
		events = append(events, ev...)
		if sc.done {
			break
		}
	}
	return events, sc.done, nil
}

func (sc *StreamConverter) handleLine(line string) ([][]byte, *errclass.Error) {
	if sc.ending {
		if data, ok := sseData(line); ok && data != "" && !shared.IsSSEDone(data) && !shared.IsJSONObject(data) {
			return nil, errclass.Translation("Chat Completions stream ended with an incomplete event payload")
		}
	}
	switch sc.sourceFormat {
	case "chat-completions":
		return sc.passthroughLine(line)
	case "messages":
		return sc.claudeLine(line)
	case "responses":
		return sc.responsesLine(line)
	default:
		return nil, shared.UnsupportedFormat(sc.sourceFormat, EndpointPath)
	}
}

// Flush terminates a stream whose upstream closed without [DONE]: the
// finish_reason chunk may legally be the last data line before close, and
// without this the client never sees message_delta/message_stop or
// response.completed — nor usage stranded behind the deferred terminal
// (FR-006). Claude synthesis appends message_stop after the held
// message_delta; Responses synthesis emits response.completed. One-shot:
// later calls return nothing, as does any call with no pending terminal.
func (sc *StreamConverter) Flush() [][]byte {
	if sc.flushed || sc.done || !sc.finished {
		return nil
	}
	sc.flushed = true
	switch sc.sourceFormat {
	case "messages":
		events := sc.claudeTerminal()
		if len(events) == 0 {
			if !sc.terminalSent {
				return nil
			}
			// The post-finish include_usage trailer already delivered the
			// terminal via claudeLine; close-without-[DONE] must still emit
			// message_stop or Anthropic clients hang until timeout.
			return [][]byte{sc.claudeEm.MessageStop()}
		}
		return append(events, sc.claudeEm.MessageStop())
	case "responses":
		return sc.responsesTerminal()
	default:
		return nil
	}
}

// Finish processes a final unterminated line and rejects EOF before a
// finish_reason or [DONE]. Existing finish-without-[DONE] remains supported.
func (sc *StreamConverter) Finish() ([][]byte, *errclass.Error) {
	if sc.done || sc.flushed {
		return nil, nil
	}
	sc.ending = true
	events, done, eErr := sc.Feed([]byte("\n"))
	if eErr != nil {
		return nil, eErr
	}
	if done {
		return events, nil
	}
	if !sc.finished {
		return nil, errclass.Translation("Chat Completions stream ended before a terminal state")
	}
	return append(events, sc.Flush()...), nil
}

// sseData extracts the single canonical data line supplied by the public
// transport boundary, which parses multiline SSE frames before dispatch.
func sseData(line string) (payload string, ok bool) {
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// passthroughLine extracts the bare data payload for openai-target emissions;
// [DONE] sets the terminal state; the public stream wrapper emits its frame.
func (sc *StreamConverter) passthroughLine(line string) ([][]byte, *errclass.Error) {
	data, ok := sseData(line)
	if !ok {
		return nil, nil
	}
	if shared.IsSSEDone(data) {
		sc.done = true
		return nil, nil
	}
	if data == "" {
		return nil, nil
	}
	chunk, eErr := sc.decodeChunk(data)
	if eErr == nil {
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				sc.finished = true
			}
		}
	}
	if eErr != nil {
		return nil, eErr
	}
	return [][]byte{[]byte(data)}, nil
}

// ---- upstream Chat Completions chunk shape ----

type ccToolCallDelta struct {
	Index    int64  `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ccDelta struct {
	shared.ReasoningFields
	Refusal     string            `json:"refusal"`
	Content     string            `json:"content"`
	ToolCalls   []ccToolCallDelta `json:"tool_calls"`
	Annotations json.RawMessage   `json:"annotations"`
}

type ccChunkChoice struct {
	Delta        ccDelta `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type ccChunk struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []ccChunkChoice `json:"choices"`
	Usage   *ccUsage        `json:"usage"`
	Error   *ccChunkError   `json:"error"`
}

// ccChunkError is an in-stream upstream error object; choice-less chunks
// carrying one are failures, not empty deltas.
type ccChunkError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// decodeChunk parses one data payload; malformed JSON yields a
// translation failure carrying only a short redacted snippet (FR-006,
// §5 security: no upstream body echo). A non-empty upstream error object
// fails the conversion with a classified error so the fail path closes
// both streams instead of the chunk being silently dropped as a
// zero-choices delta.
//
// Usage is captured here — before any empty-choices early return in the
// format converters: OpenAI's stream_options.include_usage terminal
// chunk is exactly choice-less-with-usage, and dropping it would lose
// the final counts.
func (sc *StreamConverter) decodeChunk(data string) (*ccChunk, *errclass.Error) {
	var chunk ccChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, errclass.Translation("malformed Chat Completions stream chunk: " + shared.RedactedSnippet(data))
	}
	if chunk.Error != nil {
		return nil, chunkError(chunk.Error)
	}
	if chunk.Usage != nil {
		sc.usage = chunk.Usage
	}
	return &chunk, nil
}

// chunkError classifies an in-stream Chat Completions error object onto
// FR-009 classes, mirroring the sibling adapters' stream-error paths:
// a numeric code (or rate-limit marker) maps through FromStatus so §7
// retryability semantics apply; anything else is a retryable upstream
// server failure.
func chunkError(e *ccChunkError) *errclass.Error {
	status := 0
	switch c := e.Code.(type) {
	case float64:
		status = int(c)
	case string:
		if n, err := strconv.Atoi(c); err == nil {
			status = n
		} else if strings.Contains(c, "rate_limit") {
			status = 429
		}
	}
	if status == 0 && strings.Contains(e.Type, "rate_limit") {
		status = 429
	}
	if status > 0 {
		return errclass.FromStatus(status, e.Message)
	}
	return errclass.UpstreamFallback(e.Message)
}

// ---- Chat Completions -> claude (Anthropic Messages) stream (FR-006) ----

// claudeLine synthesizes Messages events from one chunk: the first
// chunk opens message_start, delta.content opens the text content block
// lazily on first non-empty delta (tool-only streams therefore carry
// tool_use from index 0 like non-stream chatToClaude) and becomes
// text_delta (reopening a new text block if tool_use starts
// closed it), tool_calls argument fragments open tool_use
// blocks and stream input_json_delta, finish_reason closes blocks and
// holds message_delta with the mapped stop reason until the next data
// line or [DONE] (OpenAI's standard include_usage trailer follows the
// finish_reason chunk and must land in the terminal event), and [DONE]
// emits message_stop.
func (sc *StreamConverter) claudeLine(line string) ([][]byte, *errclass.Error) {
	data, ok := sseData(line)
	if !ok || data == "" {
		return nil, nil
	}
	if shared.IsSSEDone(data) {
		if sc.started && !sc.finished {
			sc.finished = true
			sc.heldFinish = "stop"
			if sc.toolsSeen {
				sc.heldFinish = "tool_calls"
			}
		}
		events, eErr := sc.drainMessages(true)
		if eErr != nil {
			return events, eErr
		}
		sc.done = true
		events = append(events, sc.stopBlocks()...)
		events = append(events, sc.claudeTerminal()...)
		return append(events, sc.claudeEm.MessageStop()), nil
	}
	chunk, eErr := sc.decodeChunk(data)
	if eErr != nil {
		return nil, eErr
	}
	var events [][]byte
	if sc.heldFinish != "" {
		// A post-finish chunk (typically the include_usage trailer)
		// flushes the deferred terminal so it carries the final counts.
		events = append(events, sc.claudeTerminal()...)
	}
	if !sc.started && len(chunk.Choices) > 0 {
		sc.started = true
		input := int64(0)
		if chunk.Usage != nil {
			input = chunk.Usage.PromptTokens
		}
		sc.claudeEm = shared.NewClaudeEventEmitter(chunk.ID, chunk.Model)
		events = append(events, sc.claudeEm.MessageStart(input))
	}
	if len(chunk.Choices) == 0 {
		return events, nil
	}
	choice := chunk.Choices[0]
	if reasoning := choice.Delta.Text(); reasoning != "" {
		sc.pending = append(sc.pending, messagesPending{kind: "thinking", text: reasoning})
	}
	if choice.Delta.Refusal != "" {
		sc.refused = true
	}
	if text := choice.Delta.Content + choice.Delta.Refusal; text != "" {
		sc.pending = append(sc.pending, messagesPending{kind: "text", text: text, offset: utf8.RuneCountInString(sc.citationText.String())})
	}
	for _, tc := range choice.Delta.ToolCalls {
		t := sc.tools[tc.Index]
		if t == nil {
			t = &streamTool{}
			sc.tools[tc.Index] = t
			sc.toolOrder = append(sc.toolOrder, tc.Index)
			sc.toolsSeen = true
			sc.pending = append(sc.pending, messagesPending{tool: t})
		}
		if tc.ID != "" && !t.announced {
			t.id = tc.ID
		}
		if tc.Function.Name != "" && !t.announced {
			t.name = mergeToolName(t.name, tc.Function.Name)
		}
		t.args.WriteString(tc.Function.Arguments)
	}
	sc.citationText.WriteString(choice.Delta.Content)
	for _, c := range sc.decodeCitations(choice.Delta.Annotations, "messages") {
		copyCitation := c
		sc.pending = append(sc.pending, messagesPending{kind: "citation", citation: &copyCitation})
	}

	if choice.FinishReason != "" && !sc.finished {
		sc.finished = true
		sc.heldFinish = choice.FinishReason
	}
	pending, eErr := sc.drainMessages(sc.finished)
	if eErr != nil {
		return events, eErr
	}
	events = append(events, pending...)
	if sc.finished {
		events = append(events, sc.stopBlocks()...)
	}
	return events, nil
}

// claudeTerminal renders the held message_delta exactly once, carrying
// the captured usage when any arrived before emission.
func (sc *StreamConverter) claudeTerminal() [][]byte {
	finish := sc.heldFinish
	sc.heldFinish = ""
	if finish == "" {
		return nil
	}
	sc.terminalSent = true
	// Observed tool calls outrank the status-derived reason, mirroring
	// non-stream chatToClaude (FR-006).
	reason := shared.TerminalReason(sc.toolsSeen, "tool_use", shared.FinishToClaudeStop(shared.FinishWithRefusal(finish, sc.refused)))
	// Always attach: sibling terminals never omit usage; fields zero when
	// upstream reported none (F-R6).
	input, output := int64(0), int64(0)
	var cacheRead, cacheWrite *int64
	if sc.usage != nil {
		input = sc.usage.PromptTokens
		output = sc.usage.CompletionTokens
		if sc.usage.PromptDetails != nil {
			cacheRead = sc.usage.PromptDetails.CachedTokens
			cacheWrite = sc.usage.PromptDetails.CacheWriteTokens
		}
	}
	return [][]byte{sc.claudeEm.MessageDelta(&reason,
		shared.ClaudeUsage(shared.ClampSubtract(input, cacheRead, cacheWrite), output, cacheRead, cacheWrite))}
}

// stopText closes the open text content block, if any.
func (sc *StreamConverter) stopText() [][]byte {
	if !sc.textOpen {
		return nil
	}
	sc.textOpen = false
	return [][]byte{sc.claudeEm.ContentBlockStop(sc.textIndex)}
}

// stopBlocks closes the text block and every open tool_use block
// exactly once; called before every content_block_start (so blocks never
// overlap) and again (as a no-op) on finish_reason and [DONE].
func (sc *StreamConverter) stopBlocks() [][]byte {
	var events [][]byte
	if sc.reasonOpen {
		sc.reasonOpen = false
		events = append(events, sc.claudeEm.ContentBlockStop(sc.reasonIndex))
	}
	events = append(events, sc.stopText()...)
	for _, idx := range sc.toolOrder {
		t := sc.tools[idx]
		if t != nil && t.announced && !t.stopped {
			t.stopped = true
			events = append(events, sc.claudeEm.ContentBlockStop(t.blockIndex))
		}
	}
	return events
}

// ---- Chat Completions -> Responses (Responses) stream (FR-006) ----

// responsesLine synthesizes Responses events: delta.content becomes
// response.output_text.delta, tool_calls argument fragments become
// response.function_call_arguments.delta keyed by item_id, and
// finish_reason holds response.completed — emitted at the next data
// line or [DONE] with prompt/completion tokens mapped to input/output
// usage (the standard include_usage trailer follows the finish_reason
// chunk); [DONE] terminates the stream.
func (sc *StreamConverter) responsesLine(line string) ([][]byte, *errclass.Error) {
	data, ok := sseData(line)
	if !ok || data == "" {
		return nil, nil
	}
	if shared.IsSSEDone(data) {
		if sc.started && !sc.finished {
			sc.finished = true
			sc.heldFinish = "stop"
			if sc.toolsSeen {
				sc.heldFinish = "tool_calls"
			}
		}
		if err := sc.validateTools(); err != nil {
			return nil, err
		}
		sc.done = true
		return sc.responsesTerminal(), nil
	}
	chunk, eErr := sc.decodeChunk(data)
	if eErr != nil {
		return nil, eErr
	}
	var events [][]byte
	if sc.heldFinish != "" {
		// A post-finish chunk (typically the include_usage trailer)
		// flushes the deferred terminal so it carries the final counts.
		events = append(events, sc.responsesTerminal()...)
	}
	if !sc.started && len(chunk.Choices) > 0 {
		sc.started = true
		sc.id = chunk.ID
		sc.model = chunk.Model
		// Lifecycle parity with the Responses protocol: announce the
		// response before any deltas reference it (F18).
		events = append(events, sc.responsesEm().Created())
	}
	if len(chunk.Choices) == 0 {
		return events, nil
	}
	choice := chunk.Choices[0]
	if reasoning := choice.Delta.Text(); reasoning != "" {
		if sc.reasonIndex < 0 {
			sc.reasonIndex = sc.nextIndex
			sc.nextIndex++
			events = append(events, sc.responsesEm().ReasoningAdded(sc.id+"_reasoning", sc.reasonIndex)...)
		}
		sc.respReason.WriteString(reasoning)
		events = append(events, sc.responsesEm().ReasoningDelta(sc.id+"_reasoning", sc.reasonIndex, reasoning))
	}
	if choice.Delta.Content != "" || choice.Delta.Refusal != "" {
		if sc.msgIndex < 0 {
			sc.msgIndex = sc.nextIndex
			sc.nextIndex++
			events = append(events, sc.responsesEm().ItemAdded(sc.msgIndex, map[string]any{
				"type": "message", "role": "assistant", "id": sc.id, "content": []any{},
			}))
		}
		if choice.Delta.Content != "" {
			sc.respText.WriteString(choice.Delta.Content)
			partIndex := 0
			if sc.refusalFirst {
				partIndex = 1
			}
			events = append(events, sc.responsesEm().TextDeltaAt(sc.id, sc.msgIndex, partIndex, choice.Delta.Content))
		}
		if choice.Delta.Refusal != "" {
			if sc.respText.Len() == 0 {
				sc.refusalFirst = true
			}
			sc.respRefusal.WriteString(choice.Delta.Refusal)
			partIndex := 0
			if !sc.refusalFirst {
				partIndex = 1
			}
			events = append(events, sc.responsesEm().RefusalDelta(sc.id, sc.msgIndex, partIndex, choice.Delta.Refusal))
		}
	}
	sc.citationText.WriteString(choice.Delta.Content)
	sc.citations = append(sc.citations, sc.decodeCitations(choice.Delta.Annotations, "responses")...)
	if sc.msgIndex >= 0 {
		for sc.citationSent < len(sc.citations) {
			i := sc.citationSent
			sc.citationSent++
			contentIndex := 0
			if sc.refusalFirst {
				contentIndex = 1
			}
			events = append(events, sc.responsesEm().AnnotationAdded(sc.id, sc.msgIndex, contentIndex, i, sc.citations[i]))
		}
	}

	for _, tc := range choice.Delta.ToolCalls {
		t := sc.tools[tc.Index]
		if t == nil {
			t = &streamTool{blockIndex: sc.nextIndex}
			sc.nextIndex++
			sc.tools[tc.Index] = t
			sc.toolsSeen = true
			sc.toolOrder = append(sc.toolOrder, tc.Index)
		}
		if !t.announced {
			if tc.ID != "" {
				t.id = tc.ID
			}
			if tc.Function.Name != "" {
				t.name = mergeToolName(t.name, tc.Function.Name)
			}
		}
		t.args.WriteString(tc.Function.Arguments)
		if t.id == "" || t.name == "" {
			continue
		}
		if !t.announced {
			t.announced = true
			events = append(events, sc.responsesEm().ItemAdded(t.blockIndex, map[string]any{"type": "function_call", "call_id": t.id, "name": t.name, "arguments": ""}))
		}
		if sc.respTools.IsShell(t.name) {
			// Shell actions are emitted as a complete output item.
		} else if sc.respTools.IsCustom(t.name) {
			if tail := shared.CustomInputTail(t.args.String(), &t.customEmitted); tail != "" {
				events = append(events, sc.responsesEm().InputDelta(t.id, t.blockIndex, tail))
			}
		} else if tail := t.args.String()[t.sent:]; tail != "" {
			events = append(events, sc.responsesEm().ArgsDelta(t.id, t.blockIndex, tail))
			t.sent = t.args.Len()
		}
	}
	if choice.FinishReason != "" && !sc.finished {
		if err := sc.validateTools(); err != nil {
			return nil, err
		}
		sc.finished = true
		sc.heldFinish = choice.FinishReason
	}
	return events, nil
}

// responsesEm binds the shared Responses emitter kernel to the captured
// upstream chunk identity so this route's frames cannot diverge from the
// sibling Messages-route synthesizer (FR-006).
func (sc *StreamConverter) responsesEm() shared.ResponsesEventEmitter {
	return shared.ResponsesEventEmitter{ID: sc.id, Model: sc.model, Tools: sc.respTools}
}

// responsesTerminal renders the held response.completed exactly once,
// carrying the captured usage when any arrived before emission.
func (sc *StreamConverter) responsesTerminal() [][]byte {
	finish := sc.heldFinish
	sc.heldFinish = ""
	if finish == "" {
		return nil
	}
	// Status derives positionally from finish alone: length→incomplete,
	// else completed. Tool calls are represented by output items, not
	// status vocabulary (F-R2, Messages-route parity).
	status := shared.ResponseStatusFromCCFinish(finish)
	// Shared assembler (FR-006 sibling parity): the terminal payload
	// carries the output items fed in ARRIVAL order so Render() reproduces
	// the streamed output_item.added indexes exactly — the message slot is
	// pinned at sc.msgIndex (assigned at the first text delta, after any
	// tools-first announcements), never displacing already-announced
	// function_call items; this mirrors the Messages-route invariant that
	// terminal output order equals announcement order (W4 pin).
	oa := shared.NewOutputAssembler(sc.id, sc.respTools)
	reserved := false
	for _, idx := range sc.toolOrder {
		t := sc.tools[idx]
		if sc.msgIndex >= 0 && !reserved && t.blockIndex > sc.msgIndex {
			oa.ReserveTextSlot()
			reserved = true
		}
		oa.AppendFunctionCall(t.id, t.name, shared.DefaultArgs(t.args.String()))
	}
	if sc.msgIndex >= 0 && !reserved {
		oa.ReserveTextSlot()
	}
	if sc.refusalFirst {
		oa.AddRefusal(sc.respRefusal.String())
		oa.AddText(sc.respText.String())
	} else {
		oa.AddText(sc.respText.String())
		oa.AddRefusal(sc.respRefusal.String())
	}
	// Always attach (F-R6): zero-valued fields when upstream sent none.
	input, outputTokens := int64(0), int64(0)
	if sc.usage != nil {
		input = sc.usage.PromptTokens
		outputTokens = sc.usage.CompletionTokens
	}
	var details shared.UsageDetails
	if sc.usage != nil {
		if sc.usage.PromptDetails != nil {
			details.CachedTokens = sc.usage.PromptDetails.CachedTokens
			details.CacheWriteTokens = sc.usage.PromptDetails.CacheWriteTokens
		}
		if sc.usage.CompletionDetails != nil {
			details.ReasoningTokens = sc.usage.CompletionDetails.ReasoningTokens
		}
	}
	usage := shared.NewResponsesUsageFrom(input, outputTokens, details)
	oa.AddCitations(sc.citations)
	items := oa.Render()
	if sc.reasonIndex >= 0 {
		item := shared.ReasoningItem(sc.id+"_reasoning", sc.respReason.String())
		items = append(items, nil)
		copy(items[sc.reasonIndex+1:], items[sc.reasonIndex:])
		items[sc.reasonIndex] = item
	}
	em := sc.responsesEm()
	var events [][]byte
	for idx, item := range items {
		v, ok := item.(shared.RespItem)
		if !ok {
			continue
		}
		switch v.Type {
		case "reasoning":
			events = append(events, em.ReasoningDone(v, idx)...)
		case "message":
			events = append(events, em.MessageDone(v, idx)...)
		case "function_call":
			events = append(events, em.ArgsDone(v.CallID, idx, v.Name, v.Arguments))
			events = append(events, em.ItemDone(idx, v))
		case "custom_tool_call":
			events = append(events, em.InputDone(v.CallID, idx, v.Input))
			events = append(events, em.ItemDone(idx, v))
		case "shell_call":
			events = append(events, em.ItemDone(idx, v))
		}
	}
	return append(events, em.Completed(status, usage, items))
}

func mergeToolName(current, next string) string {
	if next == "" || current == next || strings.HasPrefix(current, next) {
		return current
	}
	if current == "" || strings.HasPrefix(next, current) {
		return next
	}
	return current + next
}
func (sc *StreamConverter) validateTools() *errclass.Error {
	for _, tool := range sc.tools {
		if tool.id == "" || tool.name == "" {
			return errclass.Translation("Chat Completions tool ended without an id or name")
		}
		if sc.respTools.IsShell(tool.name) {
			if _, err := shared.ShellAction(tool.args.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (sc *StreamConverter) decodeCitations(raw json.RawMessage, to string) []shared.Citation {
	if sc.citationKeys == nil {
		sc.citationKeys = map[string]bool{}
	}
	var out []shared.Citation
	for _, c := range shared.DecodeCitations(raw, "chat-completions", to, "delta.annotations", sc.citationText.String(), 0, sc.respTools) {
		encoded, _ := json.Marshal(c.Wire(to))
		key := string(encoded)
		if !sc.citationKeys[key] {
			sc.citationKeys[key] = true
			out = append(out, c)
		}
	}
	return out
}
