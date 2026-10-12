package translator_test

import (
	"strings"
	"testing"

	translator "github.com/dillonzq/api-translator"
)

// Custom tool identities are restored from wire names, so a responses-side
// function_call for a registered custom tool is rewritten into the
// custom_tool_call shape. The rewrite reads arguments as a JSON string: when
// upstream sends another type, the original bytes must survive instead of
// being replaced with an empty input.
func TestCustomToolNonStringArgumentsPreserved(t *testing.T) {
	state := translator.NewRequestState()
	if _, err := translator.ConvertRequest(translator.FormatChatCompletions, "m", translator.FormatResponses,
		[]byte(`{"input":"hi","tools":[{"type":"custom","name":"exec"}]}`), state); err != nil {
		t.Fatal(err)
	}
	item := `{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":{"nested":true},"sequence_number":7}`
	response := `{"id":"r","object":"response","status":"completed","output":[` + item + `]}`
	out, err := translator.ConvertResponse(translator.FormatResponses, translator.FormatResponses, 200, []byte(response), state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"arguments":{"nested":true}`) {
		t.Fatalf("non-string arguments were dropped: %s", out)
	}
	if strings.Contains(string(out), `"input":""`) {
		t.Fatalf("empty custom input was invented: %s", out)
	}
}

// Shell calls validate their arguments as an action object, so a non-string
// value must fail instead of becoming an empty action.
func TestShellNonStringArgumentsRejectedNotInvented(t *testing.T) {
	state := translator.NewRequestState()
	if _, err := translator.ConvertRequest(translator.FormatChatCompletions, "m", translator.FormatResponses,
		[]byte(`{"input":"hi","tools":[{"type":"shell"}]}`), state); err != nil {
		t.Fatal(err)
	}
	item := `{"type":"function_call","id":"i1","call_id":"c1","name":"shell","arguments":["pwd"]}`
	response := `{"id":"r","object":"response","status":"completed","output":[` + item + `]}`
	out, err := translator.ConvertResponse(translator.FormatResponses, translator.FormatResponses, 200, []byte(response), state)
	if err == nil {
		t.Fatalf("malformed shell arguments converted without an error: %s", out)
	}
	if strings.Contains(string(out), `"action":{"commands":[]}`) {
		t.Fatalf("empty shell action was invented: %s", out)
	}
}

// A custom tool's streamed arguments-done event with a non-string arguments
// value has nothing to unwrap; with no accumulated delta either, the event
// passes through verbatim rather than announcing an empty input.
func TestCustomToolNonStringArgumentsDonePassesThrough(t *testing.T) {
	state := translator.NewRequestState()
	if _, err := translator.ConvertRequest(translator.FormatChatCompletions, "m", translator.FormatResponses,
		[]byte(`{"input":"hi","tools":[{"type":"custom","name":"exec"}]}`), state); err != nil {
		t.Fatal(err)
	}
	stream := "event: response.output_item.added\ndata: {\"item\":{\"type\":\"function_call\",\"id\":\"i1\",\"call_id\":\"c1\",\"name\":\"exec\"}}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"item_id\":\"i1\",\"call_id\":\"c1\",\"name\":\"exec\",\"arguments\":{\"nested\":true},\"sequence_number\":7}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"
	events := streamFrames(t, translator.FormatResponses, translator.FormatResponses, stream, state)
	var sawPassthrough, sawCompleted bool
	for _, e := range events {
		switch e.Name {
		case "response.custom_tool_call_input.done":
			if e.Data["input"] == "" {
				t.Fatalf("empty custom input was invented: %+v", e.Data)
			}
		case "response.function_call_arguments.done":
			sawPassthrough = true
			if _, ok := e.Data["arguments"].(map[string]any); !ok {
				t.Fatalf("arguments payload changed shape: %+v", e.Data)
			}
		case "response.completed":
			sawCompleted = true
		}
	}
	if !sawPassthrough {
		t.Fatalf("malformed arguments-done event was rewritten: %+v", events)
	}
	if !sawCompleted {
		t.Fatalf("stream did not terminate: %+v", events)
	}
}

// Tool inputs are decoded into `any` before they are re-encoded as tool call
// arguments. A float64 roundtrip rounds integers beyond 2^53 and turns
// out-of-range literals into 0, so the decoder must keep exact numbers.
func TestMessagesToolInputNumericFidelity(t *testing.T) {
	body := `{"id":"r","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"c1","name":"f","input":{"big":9007199254740993,"exp":1e400,"small":1.5}}]}`
	out, err := translator.ConvertResponse(translator.FormatMessages, translator.FormatChatCompletions, 200, []byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"9007199254740993", "1e400", "1.5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool input lost %s: %s", want, got)
		}
	}
	if strings.Contains(got, "9007199254740992") || strings.Contains(got, `"exp":0`) {
		t.Fatalf("tool input went through a float64 roundtrip: %s", got)
	}
}

// A server_tool_use block whose input is not the expected shape cannot be
// converted into a hosted search call; failing is the only option that does
// not invent an empty query.
func TestMessagesServerToolUseInvalidInputFails(t *testing.T) {
	body := `{"id":"r","model":"m","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":123}}]}`
	if _, err := translator.ConvertResponse(translator.FormatMessages, translator.FormatResponses, 200, []byte(body), nil); err == nil {
		t.Fatal("expected a translation error for a malformed web search input")
	}
	if _, err := translator.ConvertResponse(translator.FormatMessages, translator.FormatResponses, 200,
		[]byte(`{"id":"r","model":"m","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"Go"}}]}`), nil); err != nil {
		t.Fatalf("valid search input must still convert: %v", err)
	}
}

// A citations field that is not an array used to be dropped without a trace.
// The reference loss is recorded so callers can see it under every policy that
// reports diagnostics.
func TestCitationsContainerShapeIsReported(t *testing.T) {
	state := translator.NewRequestState()
	body := `{"id":"r","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"answer","citations":{"url":"https://example.com"}}]}`
	if _, err := translator.ConvertResponse(translator.FormatMessages, translator.FormatChatCompletions, 200, []byte(body), state); err != nil {
		t.Fatal(err)
	}
	for _, d := range state.Diagnostics() {
		if d.Path == "content.citations" {
			return
		}
	}
	t.Fatalf("citations loss not reported: %+v", state.Diagnostics())
}

// An inline error envelope that does not match the modeled shape is still an
// upstream failure. Reporting it as success would hand the client an empty
// completion for a failed request.
func TestUnreadableInlineErrorEnvelopeFails(t *testing.T) {
	_, err := translator.ConvertResponse(translator.FormatChatCompletions, translator.FormatMessages, 200,
		[]byte(`{"error":{"message":123}}`), nil)
	if err == nil {
		t.Fatal("unreadable error envelope was treated as success")
	}
	if err.Class != translator.ClassUpstream {
		t.Fatalf("class = %v, want %v", err.Class, translator.ClassUpstream)
	}
	// A string envelope is the other shape providers send inline.
	if _, err := translator.ConvertResponse(translator.FormatChatCompletions, translator.FormatMessages, 200,
		[]byte(`{"error":"upstream exploded"}`), nil); err == nil {
		t.Fatal("string error envelope was treated as success")
	}
}
