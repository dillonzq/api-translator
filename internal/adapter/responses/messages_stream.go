package responses

import (
	"encoding/json"
	"github.com/dillonzq/api-translator/internal/adapter/shared"
	"strings"
	"unicode/utf8"

	"github.com/dillonzq/api-translator/internal/errclass"
)

// Messages requires sequential content blocks. A tool remains at the head
// of the queue until its arguments are complete; later content waits rather
// than closing a block that may still receive argument fragments.
type messagesEmission struct {
	kind     string
	text     string
	tool     *messagesTool
	origin   string
	citation *shared.Citation
	search   *messagesSearch
}

type messagesTool struct {
	id       string
	name     string
	args     strings.Builder
	index    int
	sent     int
	opened   bool
	closed   bool
	complete bool
}

func (sc *StreamConverter) queueMessagesTool(key int, id, name, delta string, complete bool) ([][]byte, *errclass.Error) {
	if sc.messagesTools == nil {
		sc.messagesTools = map[int]*messagesTool{}
	}
	tool := sc.messagesTools[key]
	if tool == nil {
		tool = &messagesTool{id: id, name: name, index: -1}
		sc.messagesTools[key] = tool
		sc.messagesQueue = append(sc.messagesQueue, messagesEmission{kind: "tool", tool: tool})
	}
	if !tool.opened {
		// Identity can arrive across several items after parameter deltas/done.
		if id != "" {
			tool.id = id
		}
		if name != "" {
			tool.name = name
		}
	}
	if tool.closed && delta != "" {
		return nil, errclass.Translation("Responses tool arguments changed after tool completion")
	}
	tool.args.WriteString(delta)
	tool.complete = tool.complete || complete
	return sc.drainMessages(), nil
}

func (sc *StreamConverter) queueMessagesContent(kind, text string) [][]byte {
	sc.messagesQueue = append(sc.messagesQueue, messagesEmission{kind: kind, text: text})
	return sc.drainMessages()
}

func (sc *StreamConverter) drainMessages() [][]byte {
	if sc.draining {
		return nil
	}
	sc.draining = true
	defer func() { sc.draining = false }()
	var events [][]byte
	for len(sc.messagesQueue) > 0 {
		entry := sc.messagesQueue[0]
		if entry.kind == "tool" {
			tool := entry.tool
			if tool.id == "" || tool.name == "" {
				break
			}
			if !tool.opened {
				events = append(events, sc.closeOpenBlocks()...)
				tool.index = sc.allocIndex()
				tool.opened = true
				sc.openBlocks = append(sc.openBlocks, tool.index)
				events = append(events, sc.claudeChunks().ContentBlockStart(tool.index, "tool_use", map[string]any{"id": tool.id, "name": tool.name, "input": map[string]any{}}))
			}
			args := tool.args.String()
			if tool.sent < len(args) {
				events = append(events, sc.argsDelta(tool.index, args[tool.sent:]))
				tool.sent = len(args)
			}
			if !tool.complete {
				break
			}
			events = append(events, sc.closeOpenBlocks()...)
			tool.closed = true
		} else if entry.kind == "search" {
			if !entry.search.complete {
				break
			}
			if !entry.search.emitted {
				events = append(events, sc.closeOpenBlocks()...)
				for _, block := range entry.search.call.MessageBlocks() {
					index := sc.allocIndex()
					events = append(events, sc.claudeChunks().ContentBlockStart(index, block["type"].(string), block), sc.claudeChunks().ContentBlockStop(index))
				}
				entry.search.emitted = true
			}
		} else if entry.kind == "citation" {
			events = append(events, sc.messageCitation(entry)...)

		} else {
			switch entry.kind {
			case "text":
				wasOpen := sc.textOpen
				more, _, _ := sc.emitTextDelta(entry.text)
				if !wasOpen {
					sc.messagesText.Reset()
				}
				sc.messagesText.WriteString(entry.text)
				if entry.origin != "" {
					owner := sc.textPart(entry.origin)
					owner.block = sc.textIndex
					owner.closed = false
					owner.emittedRunes += utf8.RuneCountInString(entry.text)
					pending := owner.pending
					owner.pending = nil
					for _, raw := range pending {
						more = append(more, sc.annotationPart(entry.origin, raw)...)
					}
				}
				events = append(events, more...)
			case "reasoning":
				events = append(events, sc.emitReasoningDelta(entry.text)...)
			case "reasoning_stop":
				if sc.reasonOpen {
					events = append(events, sc.closeOpenBlocks()...)
				}
			}
		}
		sc.messagesQueue[0] = messagesEmission{}
		sc.messagesQueue = sc.messagesQueue[1:]
	}
	return events
}

func (sc *StreamConverter) textDelta(delta string) ([][]byte, bool, *errclass.Error) {
	if sc.source == "messages" {
		return sc.queueMessagesContent("text", delta), false, nil
	}
	return sc.emitTextDelta(delta)
}

func (sc *StreamConverter) emitReasoningDelta(text string) [][]byte {
	var events [][]byte
	if !sc.reasonOpen {
		events = append(events, sc.closeOpenBlocks()...)
		sc.reasonIndex = sc.allocIndex()
		sc.reasonOpen = true
		sc.openBlocks = append(sc.openBlocks, sc.reasonIndex)
		events = append(events, sc.claudeChunks().ContentBlockStart(sc.reasonIndex, "thinking", map[string]any{"thinking": ""}))
	}
	return append(events, sc.claudeChunks().ContentBlockDelta(sc.reasonIndex, map[string]any{"type": "thinking_delta", "thinking": text}))
}

type messagesSearch struct {
	call     shared.HostedSearch
	complete bool
	emitted  bool
}

func (sc *StreamConverter) searchItem(item functionCallItem, complete bool) ([][]byte, bool, *errclass.Error) {
	search, ok := shared.SearchFromResponse(item.Raw)
	if !ok {
		sc.respTools.Report("responses", sc.source, "output.web_search_call.action", true)
		return nil, false, nil
	}
	if sc.source == "chat-completions" {
		sc.respTools.Report("responses", sc.source, "output.web_search_call", true)
		return nil, false, nil
	}
	if search.ID == "" {
		return nil, false, errclass.Translation("web search item has no identity")
	}
	if sc.messagesSearch == nil {
		sc.messagesSearch = map[string]*messagesSearch{}
	}
	state := sc.messagesSearch[search.ID]
	if state == nil {
		state = &messagesSearch{call: search}
		sc.messagesSearch[search.ID] = state
		sc.messagesQueue = append(sc.messagesQueue, messagesEmission{kind: "search", search: state})
	}
	if state.emitted {
		before, _ := json.Marshal(state.call.ResponseItem())
		after, _ := json.Marshal(search.ResponseItem())
		if string(before) != string(after) {
			sc.respTools.Report("responses", "messages", "output.web_search_call.snapshot", false)
		}
		return nil, false, nil
	}
	if search.Query == "" {
		search.Query = state.call.Query
	}
	if len(search.Sources) == 0 {
		search.Sources = state.call.Sources
	}
	state.call = search
	state.complete = state.complete || complete
	return sc.drainMessages(), false, nil
}

// Citation delivery is shared by queued events and annotations received before
// their text. Pending annotations are emitted immediately after that text,
// before the queue can open a later tool or content block.
func (sc *StreamConverter) messageCitation(entry messagesEmission) [][]byte {
	owner := sc.textPart(entry.origin)
	if !sc.textOpen || owner.block != sc.textIndex || owner.closed || entry.citation.Text != "" && !strings.Contains(sc.messagesText.String(), entry.citation.Text) {
		sc.respTools.Report("responses", "messages", "output."+entry.origin+".annotations.closed_block", false)
		return nil
	}
	return [][]byte{sc.claudeChunks().ContentBlockDelta(owner.block, map[string]any{"type": "citations_delta", "citation": entry.citation.Wire("messages")})}
}
