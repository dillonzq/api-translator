package translator_test

import (
	"encoding/json"
	"fmt"
	"testing"

	translator "github.com/dillonzq/api-translator"
)

func TestToolDeclarationsFirstWinsAcrossNormalizationPaths(t *testing.T) {
	for _, typ := range []string{"function", "custom"} {
		for _, namespace := range []bool{false, true} {
			for _, target := range formats {
				t.Run(fmt.Sprintf("%s/namespace=%t/%s", typ, namespace, target), func(t *testing.T) {
					tool := func(description string) any {
						child := map[string]any{"type": typ, "name": "f", "description": description}
						if namespace {
							return map[string]any{"type": "namespace", "name": "ns", "tools": []any{child}}
						}
						return child
					}
					body, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": []any{tool("second")}}}, "tools": []any{tool("first")}})
					state := translator.NewRequestState()
					var out []byte
					var err *translator.Error
					if target == translator.FormatResponses {
						out, err = translator.NormalizeResponsesRequest(body, state)
					} else {
						out, err = translator.ConvertRequest(target, "m", translator.FormatResponses, body, state)
					}
					if err != nil {
						t.Fatal(err)
					}
					doc := object(t, out)
					tools := doc["tools"].([]any)
					if len(tools) != 1 {
						t.Fatalf("expected one declaration: %s", out)
					}
					declaration := tools[0].(map[string]any)
					if target == translator.FormatChatCompletions {
						declaration = declaration["function"].(map[string]any)
					}
					name := "f"
					if namespace {
						name = "ns__f"
					}
					if declaration["name"] != name || declaration["description"] != "first" {
						t.Fatalf("first declaration lost: %s", out)
					}
				})
			}
		}
	}
}

func TestToolChoiceUsesOriginalIdentityAcrossNormalizationPaths(t *testing.T) {
	for _, target := range formats {
		for _, original := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/original=%t", target, original), func(t *testing.T) {
				choice := map[string]any{"type": "function", "name": "ns__f"}
				if original {
					choice["name"] = "f"
					choice["namespace"] = "ns"
				}
				body, _ := json.Marshal(map[string]any{
					"input":       "hi",
					"tools":       []any{map[string]any{"type": "namespace", "name": "ns", "tools": []any{map[string]any{"type": "function", "name": "f"}}}},
					"tool_choice": choice,
				})
				state := translator.NewRequestState()
				var err *translator.Error
				if target == translator.FormatResponses {
					_, err = translator.NormalizeResponsesRequest(body, state)
				} else {
					_, err = translator.ConvertRequest(target, "m", translator.FormatResponses, body, state)
				}
				if (err == nil) != original {
					t.Fatalf("choice identity accepted incorrectly: original=%t err=%v", original, err)
				}
			})
		}
	}
}

func TestHistoricalToolIdentityCollisionAcrossNormalizationPaths(t *testing.T) {
	for _, typ := range []string{"function_call", "custom_tool_call"} {
		for _, reverse := range []bool{false, true} {
			for _, target := range formats {
				t.Run(fmt.Sprintf("%s/reverse=%t/%s", typ, reverse, target), func(t *testing.T) {
					items := []any{
						map[string]any{"type": typ, "namespace": "ns", "name": "f", "call_id": "a", "arguments": "{}", "input": "one"},
						map[string]any{"type": typ, "name": "ns__f", "call_id": "b", "arguments": "{}", "input": "two"},
					}
					if reverse {
						items[0], items[1] = items[1], items[0]
					}
					body, _ := json.Marshal(map[string]any{"input": items})
					state := translator.NewRequestState()
					var err *translator.Error
					if target == translator.FormatResponses {
						_, err = translator.NormalizeResponsesRequest(body, state)
					} else {
						_, err = translator.ConvertRequest(target, "m", translator.FormatResponses, body, state)
					}
					if err == nil {
						t.Fatal("colliding history accepted")
					}
					response := []byte(`{"output":[{"type":"function_call","name":"ns__f","arguments":"{}"}]}`)
					restored, err := translator.ConvertResponse(translator.FormatResponses, translator.FormatResponses, 200, response, state)
					if err != nil || string(restored) != string(response) {
						t.Fatalf("failed normalization leaked identities: %s %v", restored, err)
					}
				})
			}
		}
	}
}
