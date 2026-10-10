package chatcompletions

import (
	"github.com/dillonzq/api-translator/internal/adapter/shared"
	"github.com/dillonzq/api-translator/internal/errclass"
	"strings"
	"unicode/utf8"
)

type messagesPending struct {
	kind     string
	text     string
	tool     *streamTool
	offset   int
	citation *shared.Citation
}

// Keep later blocks queued until the current tool finishes. Chat Completions
// supplies one finish signal for all tools, so only the first can stream live.
func (sc *StreamConverter) drainMessages(complete bool) ([][]byte, *errclass.Error) {
	var events [][]byte
	for len(sc.pending) > 0 {
		entry := sc.pending[0]
		if t := entry.tool; t != nil {
			if t.id == "" || t.name == "" {
				if complete {
					return events, errclass.Translation("Chat Completions tool ended without an id or name")
				}
				return events, nil
			}
			if !t.announced {
				events = append(events, sc.stopBlocks()...)
				t.blockIndex = sc.nextIndex
				sc.nextIndex++
				t.announced = true
				t.stopped = false
				events = append(events, sc.claudeEm.ContentBlockStart(t.blockIndex, "tool_use", map[string]any{"id": t.id, "name": t.name, "input": map[string]any{}}))
			}
			if complete && t.args.Len() == 0 {
				t.args.WriteString("{}")
			}
			if tail := t.args.String()[t.sent:]; tail != "" {
				events = append(events, sc.claudeEm.ContentBlockDelta(t.blockIndex, map[string]any{"type": "input_json_delta", "partial_json": tail}))
				t.sent = t.args.Len()
			}
			if !complete {
				return events, nil
			}
			events = append(events, sc.claudeEm.ContentBlockStop(t.blockIndex))
			t.stopped = true
		} else if entry.kind == "citation" {
			c := entry.citation
			belongs := sc.textOpen
			if c.Start != nil && (*c.Start < sc.messagesTextBase || c.End != nil && *c.End > sc.messagesTextEnd) {
				belongs = false
			}
			if c.Text != "" && !strings.Contains(sc.messagesText.String(), c.Text) {
				belongs = false
			}
			if !belongs {
				sc.respTools.Report("chat-completions", "messages", "delta.annotations.closed_block", false)
			} else {
				events = append(events, sc.claudeEm.ContentBlockDelta(sc.textIndex, map[string]any{"type": "citations_delta", "citation": c.Wire("messages")}))
			}
		} else if entry.kind == "thinking" {
			if !sc.reasonOpen {
				events = append(events, sc.stopBlocks()...)
				sc.reasonIndex = sc.nextIndex
				sc.nextIndex++
				sc.reasonOpen = true
				events = append(events, sc.claudeEm.ContentBlockStart(sc.reasonIndex, "thinking", map[string]any{"thinking": ""}))
			}
			events = append(events, sc.claudeEm.ContentBlockDelta(sc.reasonIndex, map[string]any{"type": "thinking_delta", "thinking": entry.text}))
		} else {
			if !sc.textOpen {
				events = append(events, sc.stopBlocks()...)
				sc.textIndex = sc.nextIndex
				sc.nextIndex++
				sc.textOpen = true
				sc.messagesText.Reset()
				sc.messagesTextBase = entry.offset
				sc.messagesTextEnd = entry.offset
				events = append(events, sc.claudeEm.ContentBlockStart(sc.textIndex, "text", map[string]any{"text": ""}))
			}
			sc.messagesText.WriteString(entry.text)
			sc.messagesTextEnd = entry.offset + utf8.RuneCountInString(entry.text)
			events = append(events, sc.claudeEm.ContentBlockDelta(sc.textIndex, map[string]any{"type": "text_delta", "text": entry.text}))
		}
		sc.pending = sc.pending[1:]
	}
	return events, nil
}
