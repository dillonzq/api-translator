package messages

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dillonzq/api-translator/internal/adapter/shared"
	"github.com/dillonzq/api-translator/internal/errclass"
)

// StreamConverter converts an upstream Anthropic Messages SSE stream
// (typed `event:`/`data:` pairs) into the client protocol incrementally
// (FR-006, AC §C streaming). It is stateful and sequential-use only:
// one instance per upstream response, Feed called with consecutive
// network chunks in order.
type StreamConverter struct {
	framer           *shared.SSEFramer
	sourceFormat     string
	created          int64
	blocks           map[int]*blockState
	toolOrdinal      int
	msgID            string
	model            string
	promptTokens     int64
	completionTokens int64
	cacheRead        *int64
	cacheCreation    *int64
	stopReason       string // last observed message_delta stop_reason (responses terminal status)
	toolsSeen        bool   // any tool_use block observed (openai finish-reason precedence)
	roleSent         bool
	msgIdx           int  // responses: compacted output_index of the ONE announced message item (-1 until then)
	outCount         int  // responses: next compacted output position; includes visible thinking items
	emitted          bool // any client event emitted (Flush eligibility)
	done             bool
	ending           bool
	terminalSent     bool
	flushed          bool // one-shot guard for Flush
	respTools        *shared.ResponseTools
	textRunes        int
	citationKeys     map[string]bool
	citations        []shared.Citation
	search           map[string]*blockState
}

// blockState tracks one open upstream content block by index.
type blockState struct {
	kind          string
	id            string
	name          string
	ordinal       int             // compact tool_calls index for openai chunks
	outIdx        int             // responses: compacted output_index assigned at block start
	emitted       bool            // openai: id/name attached to the first arguments fragment
	text          strings.Builder // accumulated text_delta content (responses target)
	args          strings.Builder // accumulated input_json_delta content (responses target)
	customEmitted int             // custom_tool_call_input bytes already emitted as deltas
	textOffset    int
	closed        bool
	search        *shared.HostedSearch
	searchDone    bool
}

// NewStreamConverter prepares stream conversion for sourceFormat
// ("chat-completions" Chat Completions chunks, "responses" Responses events,
// "messages" verbatim passthrough).
func NewStreamConverter(sourceFormat string, tools ...*shared.ResponseTools) *StreamConverter {
	sc := &StreamConverter{
		framer:       shared.NewSSEFramer(sourceFormat == "messages"),
		sourceFormat: sourceFormat,
		created:      time.Now().Unix(),
		blocks:       map[int]*blockState{},
		msgIdx:       -1,
	}
	sc.respTools = shared.ResponseToolContext(tools)
	sc.search = map[string]*blockState{}
	sc.citationKeys = map[string]bool{}
	return sc
}

// Feed consumes one network chunk and returns fully framed client events,
// whether the upstream stream is finished (message_stop seen), and a
// classified error. Partial lines are buffered until complete.
func (sc *StreamConverter) Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error) {
	if sc.done {
		return nil, true, nil
	}
	sc.framer.Push(chunk)
	for {
		etype, data, rawBlock, ok := sc.framer.Next()
		if !ok {
			return events, false, nil
		}
		d, eErr := sc.dispatch(etype, data, rawBlock, &events)
		if len(events) > 0 {
			sc.emitted = true
		}
		if eErr != nil {
			return events, false, eErr
		}
		if d {
			sc.done = true
			return events, true, nil
		}
	}
}

// dispatch routes one complete SSE frame. Conversion targets parse the
// data payload eagerly into typed per-event structs (with the
// type-from-payload fallback when the event: line was absent); claude
// passthrough forwards the verbatim raw block and parses only what error
// handling and done-detection require.
func (sc *StreamConverter) dispatch(etype, data string, rawBlock []byte, events *[][]byte) (bool, *errclass.Error) {
	if (etype == "message_stop" || sc.ending && data != "") && !shared.IsJSONObject(data) {
		return false, errclass.Translation("Messages stream has an incomplete event payload")
	}
	// Validate the discriminator before either native passthrough or synthesis
	// can mark completion. Extra fields remain forward-compatible.
	if etype == "message_stop" {
		var terminal struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(data), &terminal) != nil || terminal.Type != "message_stop" {
			return false, errclass.Translation("Messages message_stop payload must have type message_stop")
		}
	}
	if sc.sourceFormat == "messages" {
		return sc.dispatchClaude(etype, data, rawBlock, events)
	}
	var ev sseEvent
	if data != "" {
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return false, errclass.Translation(fmt.Sprintf("malformed SSE data JSON: %s", shared.RedactedSnippet(data)))
		}
		if etype == "" {
			etype = ev.Type // tolerate missing event: line
		}
	}
	if etype == "message_delta" && ev.Delta.StopReason != "" {
		sc.stopReason = ev.Delta.StopReason
	}
	switch sc.sourceFormat {
	case "chat-completions":
		return sc.dispatchOpenAI(etype, &ev, events)
	case "responses":
		return sc.dispatchResponses(etype, &ev, events)
	default:
		return false, shared.UnsupportedFormat(sc.sourceFormat, EndpointPath)
	}
}

// sseEvent is the typed union of Anthropic Messages SSE payload shapes on
// conversion targets. One struct serves every event type because the
// type may arrive only inside the payload (missing event: line), so the
// decode must precede routing; absent members stay zero-valued at no
// cost, and unknown keys are ignored. Error stays a raw map because the
// classifier reads an open-ended upstream object.
type sseEvent struct {
	Type         string         `json:"type"`
	Index        float64        `json:"index"`
	Message      startMessage   `json:"message"`
	ContentBlock contentBlock   `json:"content_block"`
	Delta        deltaBody      `json:"delta"`
	Usage        usageCounts    `json:"usage"`
	Error        map[string]any `json:"error"`
}

type startMessage struct {
	ID    string      `json:"id"`
	Model string      `json:"model"`
	Usage usageCounts `json:"usage"`
}

type contentBlock struct {
	Thinking  string          `json:"thinking"`
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Citations json.RawMessage `json:"citations"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	ToolUseID string          `json:"tool_use_id"`
}

type deltaBody struct {
	Thinking    string          `json:"thinking"`
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	PartialJSON string          `json:"partial_json"`
	StopReason  string          `json:"stop_reason"`
	Citation    json.RawMessage `json:"citation"`
}

type usageCounts struct {
	InputTokens   float64  `json:"input_tokens"`
	OutputTokens  float64  `json:"output_tokens"`
	CacheRead     *float64 `json:"cache_read_input_tokens"`
	CacheCreation *float64 `json:"cache_creation_input_tokens"`
}

// dispatchClaude passes event blocks through verbatim; an in-stream error
// event becomes a classified error instead of reaching the client raw.
// The payload is parsed lazily: only when the typed fallback must resolve
// a missing event: line or when the frame is an error event.
func (sc *StreamConverter) dispatchClaude(etype, data string, raw []byte, events *[][]byte) (bool, *errclass.Error) {
	var payload map[string]any
	if etype == "" && data != "" {
		json.Unmarshal([]byte(data), &payload) // best-effort type probe
		etype, _ = payload["type"].(string)
	}
	if etype == "message_delta" {
		if payload == nil {
			json.Unmarshal([]byte(data), &payload)
		}
		if delta, ok := payload["delta"].(map[string]any); ok {
			if stop, ok := delta["stop_reason"].(string); ok && stop != "" {
				sc.stopReason = stop
			}
		}
	}
	if etype == "error" {
		if payload == nil && data != "" {
			json.Unmarshal([]byte(data), &payload)
		}
		return false, sseError(mget(payload, "error"))
	}
	if len(raw) > 0 {
		*events = append(*events, raw)
	}
	return etype == "message_stop", nil
}

// dispatchOpenAI synthesizes chat.completion.chunk frames (FR-006).
func (sc *StreamConverter) dispatchOpenAI(etype string, ev *sseEvent, events *[][]byte) (bool, *errclass.Error) {
	switch etype {
	case "message_start":
		sc.msgID = ev.Message.ID
		sc.model = ev.Message.Model
		sc.promptTokens = int64(ev.Message.Usage.InputTokens)
		sc.captureCache(ev.Message.Usage)
		sc.emitRole(events)
	case "content_block_start":
		bs := &blockState{kind: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name, textOffset: sc.textRunes}
		if bs.kind == "text" {
			bs.text.WriteString(ev.ContentBlock.Text)
			sc.textRunes += utf8.RuneCountInString(ev.ContentBlock.Text)
		}
		if bs.kind == "server_tool_use" && bs.name == "web_search" {
			var input struct {
				Query string `json:"query"`
			}
			json.Unmarshal(ev.ContentBlock.Input, &input)
			bs.search = &shared.HostedSearch{ID: bs.id, Query: input.Query, Status: "in_progress"}
			sc.search[bs.id] = bs
		}
		if bs.kind == "tool_use" {
			bs.ordinal = sc.toolOrdinal
			sc.toolOrdinal++
			sc.toolsSeen = true
		}
		sc.blocks[int(ev.Index)] = bs
		if bs.search != nil || bs.kind == "web_search_tool_result" {
			sc.respTools.Report("messages", "chat-completions", "content."+bs.kind, true)
		}
		if bs.kind == "text" && ev.ContentBlock.Text != "" {
			sc.emitRole(events)
			sc.emitChunk(events, map[string]any{"content": ev.ContentBlock.Text}, "", false)
		}
		sc.emitCitations(bs, ev.ContentBlock.Citations, events)

		if bs.kind == "thinking" && ev.ContentBlock.Thinking != "" {
			sc.emitRole(events)
			sc.emitChunk(events, map[string]any{"reasoning_content": ev.ContentBlock.Thinking}, "", false)
		}
	case "content_block_delta":
		bs := sc.blocks[int(ev.Index)]
		switch ev.Delta.Type {
		case "text_delta":
			if bs != nil {
				bs.text.WriteString(ev.Delta.Text)
				sc.textRunes += utf8.RuneCountInString(ev.Delta.Text)
			}
			sc.emitRole(events)
			sc.emitChunk(events, map[string]any{"content": ev.Delta.Text}, "", false)
		case "citations_delta":
			sc.emitCitations(bs, json.RawMessage("["+string(ev.Delta.Citation)+"]"), events)
		case "input_json_delta":
			if bs != nil && bs.search != nil {
				bs.args.WriteString(ev.Delta.PartialJSON)
				break
			}
			if bs == nil || bs.kind != "tool_use" {
				return false, errclass.Translation("tool arguments fragment without a tool_use block start")
			}
			var tc map[string]any
			if !bs.emitted {
				tc = shared.CCToolCallOpeningEntry(bs.ordinal, bs.id, bs.name, ev.Delta.PartialJSON)
				bs.emitted = true
			} else {
				tc = map[string]any{
					"index":    bs.ordinal,
					"function": map[string]any{"arguments": ev.Delta.PartialJSON},
				}
			}
			sc.emitRole(events)
			sc.emitChunk(events, map[string]any{"tool_calls": []any{tc}}, "", false)
		case "thinking_delta":
			if bs != nil && bs.kind == "thinking" {
				sc.emitRole(events)
				sc.emitChunk(events, map[string]any{"reasoning_content": ev.Delta.Thinking}, "", false)
			}
		}
	case "content_block_stop":
		if bs := sc.blocks[int(ev.Index)]; bs != nil {
			bs.closed = true
		}
	case "message_delta":
		if ev.Usage.InputTokens != 0 {
			sc.promptTokens = int64(ev.Usage.InputTokens)
		}
		sc.completionTokens = int64(ev.Usage.OutputTokens)
		sc.captureCache(ev.Usage)
		// Observed tool calls outrank the status-derived reason: a
		// terminal max_tokens cannot downgrade tool_calls to length.
		if sc.stopReason == "" || sc.terminalSent {
			break
		}
		sc.terminalSent = true
		sc.emitChunk(events, map[string]any{},
			shared.TerminalReason(sc.toolsSeen, "tool_calls",
				shared.ClaudeStopToFinish(ev.Delta.StopReason)), true)
	case "message_stop":
		return true, nil
	case "error":
		return false, sseError(ev.Error)
	default: // ping and unknown event types are ignored (forward compat)
	}
	return false, nil
}

// dispatchResponses synthesizes Responses SSE events (FR-006).
func (sc *StreamConverter) dispatchResponses(etype string, ev *sseEvent, events *[][]byte) (bool, *errclass.Error) {
	switch etype {
	case "message_start":
		sc.msgID = ev.Message.ID
		sc.model = ev.Message.Model
		sc.promptTokens = int64(ev.Message.Usage.InputTokens)
		sc.captureCache(ev.Message.Usage)
		// Lifecycle parity with the Chat Completions route's synthesizer:
		// announce the response before any deltas reference it (F18).
		*events = append(*events, sc.responsesEm().Created())
	case "content_block_start":
		bs := &blockState{kind: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name, textOffset: sc.textRunes}
		if bs.kind == "text" {
			bs.text.WriteString(ev.ContentBlock.Text)
			sc.textRunes += utf8.RuneCountInString(ev.ContentBlock.Text)
		}
		if bs.kind == "server_tool_use" && bs.name == "web_search" {
			var input struct {
				Query string `json:"query"`
			}
			json.Unmarshal(ev.ContentBlock.Input, &input)
			bs.search = &shared.HostedSearch{ID: bs.id, Query: input.Query, Status: "in_progress"}
			sc.search[bs.id] = bs
		}
		sc.blocks[int(ev.Index)] = bs
		// Announce each translatable item before its first delta (F18
		// lifecycle parity), including visible thinking blocks. Indexes come
		// from the SAME compacted
		// space outputItems() renders, so streamed output_index values
		// always match terminal response.completed.output positions.
		switch bs.kind {
		case "thinking":
			bs.id = fmt.Sprintf("%s_reasoning_%d", sc.msgID, int(ev.Index))
			bs.outIdx = sc.outCount
			sc.outCount++
			*events = append(*events, sc.responsesEm().ReasoningAdded(bs.id, bs.outIdx)...)
			bs.text.WriteString(ev.ContentBlock.Thinking)
			if ev.ContentBlock.Thinking != "" {
				*events = append(*events, sc.responsesEm().ReasoningDelta(bs.id, bs.outIdx, ev.ContentBlock.Thinking))
			}
		case "text":
			if sc.msgIdx >= 0 {
				break // one message item aggregates every text block (canonical shape)
			}
			sc.msgIdx = sc.outCount
			sc.outCount++
			// Native-compatible shape: the announced item carries "id" so
			// downstream output_text.delta item_ids resolve (F-R3 parity).
			*events = append(*events, sc.responsesEm().ItemAdded(sc.msgIdx, map[string]any{
				"type": "message", "id": sc.msgID, "role": "assistant", "content": []any{},
			}))
		case "server_tool_use":
			if bs.search == nil {
				return false, errclass.Translation("unsupported server tool")
			}
			bs.outIdx = sc.outCount
			sc.outCount++
			*events = append(*events, sc.responsesEm().ItemAdded(bs.outIdx, bs.search.ResponseItem()))
		case "web_search_tool_result":
			if owner := sc.search[ev.ContentBlock.ToolUseID]; owner != nil {
				shared.ReadSearchResults(owner.search, ev.ContentBlock.Content, "messages", "responses", "content.web_search_tool_result", sc.respTools)
				*events = append(*events, sc.responsesEm().SearchDone(owner.outIdx, *owner.search)...)
				owner.searchDone = true
			} else {
				sc.respTools.Report("messages", "responses", "content.web_search_tool_result", true)
			}
		case "tool_use":
			sc.toolsSeen = true
			bs.outIdx = sc.outCount
			sc.outCount++
			*events = append(*events, sc.responsesEm().ItemAdded(bs.outIdx, map[string]any{
				"type": "function_call", "call_id": bs.id, "name": bs.name, "arguments": "",
			}))
		}
		if bs.kind == "text" && ev.ContentBlock.Text != "" {
			*events = append(*events, sc.responsesEm().TextDelta(sc.msgID, sc.msgIdx, ev.ContentBlock.Text))
		}
		sc.emitCitations(bs, ev.ContentBlock.Citations, events)

	case "content_block_delta":
		idx := int(ev.Index)
		bs := sc.blocks[idx]
		switch ev.Delta.Type {
		case "text_delta":
			if bs != nil {
				bs.text.WriteString(ev.Delta.Text)
				sc.textRunes += utf8.RuneCountInString(ev.Delta.Text)
			}
			// Deltas target the single aggregated message item, so later
			// text blocks reference its announced position, never their own.
			dIdx := idx
			if sc.msgIdx >= 0 {
				dIdx = sc.msgIdx
			}
			*events = append(*events, sc.responsesEm().TextDelta(sc.msgID, dIdx, ev.Delta.Text))
		case "citations_delta":
			sc.emitCitations(bs, json.RawMessage("["+string(ev.Delta.Citation)+"]"), events)
		case "input_json_delta":
			if bs != nil && bs.search != nil {
				bs.args.WriteString(ev.Delta.PartialJSON)
				break
			}
			if bs == nil || bs.kind != "tool_use" {
				return false, errclass.Translation("tool arguments fragment without a tool_use block start")
			}
			bs.args.WriteString(ev.Delta.PartialJSON)
			if sc.respTools.IsCustom(bs.name) {
				// Same wrapped-JSON accumulation as the Chat
				// Completions route: raw fragments never reach the
				// client, only newly-unwrapped input tails.
				if tail := shared.CustomInputTail(bs.args.String(), &bs.customEmitted); tail != "" {
					*events = append(*events, sc.responsesEm().InputDelta(bs.id, bs.outIdx, tail))
				}
			} else {
				*events = append(*events, sc.responsesEm().ArgsDelta(bs.id, bs.outIdx, ev.Delta.PartialJSON))
			}
		case "thinking_delta":
			if bs != nil && bs.kind == "thinking" {
				bs.text.WriteString(ev.Delta.Thinking)
				*events = append(*events, sc.responsesEm().ReasoningDelta(bs.id, bs.outIdx, ev.Delta.Thinking))
			}
		}
	case "content_block_stop":
		if bs := sc.blocks[int(ev.Index)]; bs != nil {
			bs.closed = true
			if bs.search != nil && bs.args.Len() > 0 {
				var input struct {
					Query string `json:"query"`
				}
				if json.Unmarshal([]byte(bs.args.String()), &input) != nil {
					return false, errclass.Translation("invalid web search input")
				}
				bs.search.Query = input.Query
				bs.search.Status = "searching"
			}
		}
	case "message_delta":
		if ev.Usage.InputTokens != 0 {
			sc.promptTokens = int64(ev.Usage.InputTokens)
		}
		sc.completionTokens = int64(ev.Usage.OutputTokens) // accumulated, reported on completion
		sc.captureCache(ev.Usage)
		if ev.Delta.StopReason != "" {
			sc.stopReason = ev.Delta.StopReason
		}
	case "message_stop":
		*events = append(*events, sc.responsesCompleted()...)
		return true, nil
	case "error":
		return false, sseError(ev.Error)
	default: // ping and unknown event types are ignored (forward compat)
	}
	return false, nil
}

// responsesCompleted builds the terminal event sequence: item lifecycle
// completion events for every rendered output item followed by
// response.completed — shape parity with the non-stream
// claudeToResponses converter for the terminal payload, with status from
// the Responses vocabulary {completed, incomplete} via the observed
// stop_reason (max_tokens truncation → incomplete); tool calls are
// represented by their function_call output items and never override the
// status (FR-006). Shared by message_stop and Flush so an early upstream
// close cannot diverge from the normal-path shape.
func (sc *StreamConverter) responsesCompleted() [][]byte {
	status := shared.ResponseStatusFromClaudeStop(sc.stopReason)
	usage := shared.NewResponsesUsageFrom(sc.promptTokens+valueOrZero(sc.cacheRead)+valueOrZero(sc.cacheCreation), sc.completionTokens,
		shared.UsageDetails{CachedTokens: sc.cacheRead, CacheWriteTokens: sc.cacheCreation})
	items := sc.outputItems()
	em := sc.responsesEm()
	out := make([][]byte, 0, len(items)*2+1)
	for idx, item := range items {
		if hosted, ok := item.(map[string]any); ok && hosted["type"] == "web_search_call" {
			id, _ := hosted["id"].(string)
			if bs := sc.search[id]; bs != nil && !bs.searchDone {
				out = append(out, em.SearchDone(idx, *bs.search)...)
				bs.searchDone = true
			}
			continue
		}
		v, ok := item.(shared.RespItem)
		if !ok {
			continue
		}
		switch v.Type {
		case "reasoning":
			out = append(out, em.ReasoningDone(v, idx)...)
		case "message":
			text := responsesMessageText(v.Content)
			_ = text
			out = append(out, em.MessageDone(v, idx)...)
		case "function_call":
			out = append(out, em.ArgsDone(v.CallID, idx, v.Name, v.Arguments), em.ItemDone(idx, v))
		case "custom_tool_call":
			out = append(out, em.InputDone(v.CallID, idx, v.Input), em.ItemDone(idx, v))
		}
	}
	return append(out, em.Completed(status, usage, items))
}

// responsesMessageText extracts the aggregated output_text from a rendered
// message item's content parts.
func responsesMessageText(content []byte) string {
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// responsesEm binds the shared Responses emitter kernel to the captured
// upstream message identity so this route's frames cannot diverge from the
// sibling Chat-Completions-route synthesizer (FR-006).
func (sc *StreamConverter) responsesEm() shared.ResponsesEventEmitter {
	return shared.ResponsesEventEmitter{ID: sc.msgID, Model: sc.model, Tools: sc.respTools}
}

// Flush emits a deferred Responses terminal only after stop_reason was
// observed. EOF without a terminal state must be checked with Finish. Native
// Messages and Chat Completions have no deferred events. One-shot, and a
// previously observed message_stop prevents any duplicate terminal.
func (sc *StreamConverter) Flush() [][]byte {
	if sc.flushed || sc.done {
		return nil
	}
	if !sc.emitted || sc.stopReason == "" {
		return nil
	}
	sc.flushed = true
	switch sc.sourceFormat {
	case "responses":
		return sc.responsesCompleted()
	case "chat-completions":
		return nil
	default: // claude passthrough forwards verbatim; nothing deferred
		return nil
	}
}

// Finish accepts EOF only after a terminal state was observed. A missing
// message_stop after stop_reason is compatible; partial output without either
// is a stream error, never a synthesized response.completed.
func (sc *StreamConverter) Finish() ([][]byte, *errclass.Error) {
	if sc.done || sc.flushed {
		return nil, nil
	}
	sc.ending = true
	sc.framer.End()
	events, done, eErr := sc.Feed(nil)
	if eErr != nil {
		return nil, eErr
	}
	if done {
		return events, nil
	}
	if sc.stopReason == "" {
		return nil, errclass.Translation("Messages stream ended before a terminal state")
	}
	return append(events, sc.Flush()...), nil
}

// outputItems materializes the Responses output array from observed
// content blocks in upstream index order (FR-006) through the shared
// OutputAssembler: ALL text blocks aggregate into ONE message item
// positioned at the first text block's slot while tool_use blocks become
// separate function_call items — the canonical native shape, equal to the
// non-stream claudeToResponses converter (FR-006 mode parity); thinking
// blocks become reasoning summaries.
func (sc *StreamConverter) outputItems() []any {
	indexes := make([]int, 0, len(sc.blocks))
	for i := range sc.blocks {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	oa := shared.NewOutputAssembler(sc.msgID, sc.respTools)
	for _, i := range indexes {
		bs := sc.blocks[i]
		switch bs.kind {
		case "thinking":
			oa.AppendReasoning(bs.id, bs.text.String())
		case "text":
			oa.ReserveTextSlot()
			oa.AddText(bs.text.String())
		case "server_tool_use":
			if bs.search != nil {
				shared.FinalizeSearch(bs.search, sc.respTools)
				oa.AppendItem(bs.search.ResponseItem())
			}
		case "tool_use":
			oa.AppendFunctionCall(bs.id, bs.name, shared.DefaultArgs(bs.args.String()))
		}
	}
	oa.AddCitations(sc.citations)
	return oa.Render()
}

// chatChunks binds the shared chunk kernel to the captured upstream
// message identity so this route's frames cannot diverge from the
// sibling Responses-route synthesizer (FR-006).
func (sc *StreamConverter) chatChunks() shared.ChatChunkBuilder {
	return shared.ChatChunkBuilder{ID: sc.msgID, Model: sc.model, Created: sc.created}
}

// emitRole guarantees the leading assistant-role chunk openai clients expect.
func (sc *StreamConverter) emitRole(events *[][]byte) {
	if sc.roleSent {
		return
	}
	sc.roleSent = true
	*events = append(*events, sc.chatChunks().RoleChunk())
}

// emitChunk appends one synthesized chat.completion.chunk frame via the
// shared kernel; an empty finish renders null (intermediate chunks) and
// withUsage attaches the accumulated token usage to the terminal frame.
func (sc *StreamConverter) emitChunk(events *[][]byte, delta map[string]any, finish string, withUsage bool) {
	b := sc.chatChunks()
	if finish == "" {
		*events = append(*events, b.Delta(delta))
		return
	}
	var usage map[string]any
	if withUsage {
		usage = shared.CCUsageFrom(sc.promptTokens+valueOrZero(sc.cacheRead)+valueOrZero(sc.cacheCreation), sc.completionTokens,
			shared.UsageDetails{CachedTokens: sc.cacheRead, CacheWriteTokens: sc.cacheCreation})
	}
	*events = append(*events, b.Finish(finish, usage))
}

func (sc *StreamConverter) captureCache(u usageCounts) {
	if u.CacheRead != nil {
		v := int64(*u.CacheRead)
		sc.cacheRead = &v
	}
	if u.CacheCreation != nil {
		v := int64(*u.CacheCreation)
		sc.cacheCreation = &v
	}
}

// sseError maps an Anthropic in-stream error event onto the shared
// classification (FR-009) via its documented error-type taxonomy.
func sseError(e map[string]any) *errclass.Error {
	errorType := sget(e, "type")
	message := sget(e, "message")
	if strings.Contains(strings.ToLower(errorType), "gousagelimiterror") || strings.Contains(strings.ToLower(errorType), "freeusagelimiterror") {
		message = errorType + ": " + message
	}
	return errclass.FromStatus(anthropicErrorStatus(errorType), message)
}

func anthropicErrorStatus(t string) int {
	switch t {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "overloaded_error":
		return 503
	default:
		return 500
	}
}

func mget(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func sget(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func (sc *StreamConverter) emitCitations(block *blockState, raw json.RawMessage, events *[][]byte) {
	if block == nil || block.kind != "text" || len(raw) == 0 {
		return
	}
	citations := shared.DecodeCitations(raw, "messages", sc.sourceFormat, "content.citations", block.text.String(), block.textOffset, sc.respTools)
	for _, c := range citations {
		encoded, _ := json.Marshal(c.Wire(sc.sourceFormat))
		key := fmt.Sprintf("%d/%s", block.textOffset, encoded)
		if sc.citationKeys[key] {
			continue
		}
		sc.citationKeys[key] = true
		if sc.sourceFormat == "chat-completions" {
			sc.emitRole(events)
			sc.emitChunk(events, map[string]any{"annotations": shared.CitationValues([]shared.Citation{c}, "chat-completions")}, "", false)
		} else {
			index := len(sc.citations)
			sc.citations = append(sc.citations, c)
			*events = append(*events, sc.responsesEm().AnnotationAdded(sc.msgID, sc.msgIdx, 0, index, c))
		}
	}
}
