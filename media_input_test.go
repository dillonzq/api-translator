package translator_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	translator "github.com/dillonzq/api-translator"
)

func mediaRequest(format, role, parts string) []byte {
	if format == translator.FormatResponses {
		return []byte(`{"model":"client","input":[{"role":"` + role + `","content":` + parts + `}]}`)
	}
	return []byte(`{"model":"client","messages":[{"role":"` + role + `","content":` + parts + `}]}`)
}

func mediaContent(t *testing.T, format string, body []byte) any {
	t.Helper()
	doc := object(t, body)
	if doc["model"] != "upstream" {
		t.Fatalf("model was not rewritten: %s", body)
	}
	key := "messages"
	if format == translator.FormatResponses {
		key = "input"
	}
	return doc[key].([]any)[0].(map[string]any)["content"]
}

func TestMediaInputConversions(t *testing.T) {
	const chatFile = `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg==","filename":"a.pdf"}}`
	const respFile = `{"type":"input_file","file_data":"data:application/pdf;base64,JVBERg==","filename":"a.pdf"}`
	const document = `{"type":"document","title":"a.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}}`
	const chatAudio = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
	const chatVideo = `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAA","processing":"static"}}`
	const respVideo = `{"type":"input_video","video_url":{"url":"data:video/mp4;base64,AAAA","processing":"static"}}`
	for _, tc := range []struct{ name, from, to, input, want string }{
		{"chat files to responses", translator.FormatChatCompletions, translator.FormatResponses, chatFile, respFile},
		{"responses files to chat", translator.FormatResponses, translator.FormatChatCompletions, respFile, chatFile},
		{"chat files to messages", translator.FormatChatCompletions, translator.FormatMessages, chatFile, document},
		{"responses files to messages", translator.FormatResponses, translator.FormatMessages, respFile, document},
		{"messages files to chat", translator.FormatMessages, translator.FormatChatCompletions, document, chatFile},
		{"messages files to responses", translator.FormatMessages, translator.FormatResponses, document, respFile},
		{"chat audio to responses", translator.FormatChatCompletions, translator.FormatResponses, chatAudio, chatAudio},
		{"responses audio to chat", translator.FormatResponses, translator.FormatChatCompletions, chatAudio, chatAudio},
		{"flat responses audio", translator.FormatResponses, translator.FormatChatCompletions, `{"type":"input_audio","data":"UklGRg==","format":"wav"}`, chatAudio},
		{"chat video to responses", translator.FormatChatCompletions, translator.FormatResponses, chatVideo, respVideo},
		{"responses video to chat", translator.FormatResponses, translator.FormatChatCompletions, respVideo, chatVideo},
		{"video string and processing override", translator.FormatResponses, translator.FormatChatCompletions, `{"type":"input_video","video_url":"data:video/mp4;base64,AAAA","processing":"static"}`, chatVideo},
		{"video nested processing override", translator.FormatResponses, translator.FormatChatCompletions, `{"type":"input_video","video_url":{"url":"data:video/mp4;base64,AAAA","processing":"agentic"},"processing":"static"}`, chatVideo},
		{"file id to responses", translator.FormatChatCompletions, translator.FormatResponses, `{"type":"file","file":{"file_id":"file-1"}}`, `{"type":"input_file","file_id":"file-1"}`},
		{"file id to chat", translator.FormatResponses, translator.FormatChatCompletions, `{"type":"input_file","file_id":"file-1"}`, `{"type":"file","file":{"file_id":"file-1"}}`},
		{"file URL to messages", translator.FormatResponses, translator.FormatMessages, `{"type":"input_file","file_url":"https://example.test/a.pdf"}`, `{"type":"document","source":{"type":"url","url":"https://example.test/a.pdf"}}`},
		{"document URL to responses", translator.FormatMessages, translator.FormatResponses, `{"type":"document","source":{"type":"url","url":"https://example.test/a.pdf"}}`, `{"type":"input_file","file_url":"https://example.test/a.pdf"}`},
		{"text document", translator.FormatMessages, translator.FormatResponses, `{"type":"document","source":{"type":"text","data":"hello"}}`, `{"type":"input_file","file_data":"data:text/plain;base64,aGVsbG8="}`},
		{"chat text file to messages", translator.FormatChatCompletions, translator.FormatMessages, `{"type":"file","file":{"file_data":"data:text/plain;base64,aGVsbG8=","filename":"a.txt"}}`, `{"type":"document","title":"a.txt","source":{"type":"text","media_type":"text/plain","data":"hello"}}`},
		{"responses text file to messages", translator.FormatResponses, translator.FormatMessages, `{"type":"input_file","file_data":"data:text/plain;base64,aGVsbG8=","filename":"a.txt"}`, `{"type":"document","title":"a.txt","source":{"type":"text","media_type":"text/plain","data":"hello"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := translator.NewRequestState()
			state.Options.LossPolicy = translator.LossPolicyStrict
			textType, targetText := "text", "text"
			if tc.from == translator.FormatResponses {
				textType = "input_text"
			}
			if tc.to == translator.FormatResponses {
				targetText = "input_text"
			}
			input := `[{"type":"` + textType + `","text":"before"},` + tc.input + `,{"type":"` + textType + `","text":"after"}]`
			body, err := translator.ConvertRequest(tc.to, "upstream", tc.from, mediaRequest(tc.from, "user", input), state)
			if err != nil {
				t.Fatal(err)
			}
			var want any
			json.Unmarshal([]byte(`[{"type":"`+targetText+`","text":"before"},`+tc.want+`,{"type":"`+targetText+`","text":"after"}]`), &want)
			if got := mediaContent(t, tc.to, body); !reflect.DeepEqual(got, want) {
				t.Fatalf("content = %v, want %v", got, want)
			}
			if len(state.Diagnostics()) != 0 {
				t.Fatalf("unexpected losses: %v", state.Diagnostics())
			}
		})
	}
}

func TestMediaInputRejections(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, role, part string
		class                      translator.Class
	}{
		{"messages audio", translator.FormatChatCompletions, translator.FormatMessages, "user", `{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}`, translator.ClassUnsupported},
		{"messages video", translator.FormatResponses, translator.FormatMessages, "user", `{"type":"input_video","video_url":"https://example.test/a.mp4"}`, translator.ClassUnsupported},
		{"messages file id", translator.FormatResponses, translator.FormatMessages, "user", `{"type":"input_file","file_id":"file-1"}`, translator.ClassUnsupported},
		{"chat URL file", translator.FormatResponses, translator.FormatChatCompletions, "user", `{"type":"input_file","file_url":"https://example.test/a.pdf"}`, translator.ClassUnsupported},
		{"chat document URL", translator.FormatMessages, translator.FormatChatCompletions, "user", `{"type":"document","source":{"type":"url","url":"https://example.test/a.pdf"}}`, translator.ClassUnsupported},
		{"missing file", translator.FormatChatCompletions, translator.FormatResponses, "user", `{"type":"file"}`, translator.ClassTranslation},
		{"empty file", translator.FormatResponses, translator.FormatChatCompletions, "user", `{"type":"input_file"}`, translator.ClassTranslation},
		{"audio missing data", translator.FormatResponses, translator.FormatChatCompletions, "user", `{"type":"input_audio","format":"wav"}`, translator.ClassTranslation},
		{"video numeric URL", translator.FormatResponses, translator.FormatChatCompletions, "user", `{"type":"input_video","video_url":123}`, translator.ClassTranslation},
		{"video missing URL", translator.FormatResponses, translator.FormatChatCompletions, "user", `{"type":"input_video"}`, translator.ClassTranslation},
		{"document missing data", translator.FormatMessages, translator.FormatResponses, "user", `{"type":"document","source":{"type":"base64"}}`, translator.ClassTranslation},
		{"document invalid base64", translator.FormatMessages, translator.FormatResponses, "user", `{"type":"document","source":{"type":"base64","data":"!"}}`, translator.ClassTranslation},
		{"file invalid base64", translator.FormatResponses, translator.FormatMessages, "user", `{"type":"input_file","file_data":"data:application/pdf;base64,!"}`, translator.ClassTranslation},
		{"file malformed data URL", translator.FormatResponses, translator.FormatMessages, "user", `{"type":"input_file","file_data":"data:application/pdf,abc"}`, translator.ClassTranslation},
		{"document unsupported source", translator.FormatMessages, translator.FormatResponses, "user", `{"type":"document","source":{"type":"content"}}`, translator.ClassUnsupported},
		{"system file", translator.FormatResponses, translator.FormatChatCompletions, "system", `{"type":"input_file","file_id":"file-1"}`, translator.ClassUnsupported},
		{"assistant file to responses", translator.FormatChatCompletions, translator.FormatResponses, "assistant", `{"type":"file","file":{"file_id":"file-1"}}`, translator.ClassUnsupported},
		{"assistant document to messages", translator.FormatResponses, translator.FormatMessages, "assistant", `{"type":"input_file","file_data":"data:application/pdf;base64,JVBERg=="}`, translator.ClassUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := translator.ConvertRequest(tc.to, "upstream", tc.from, mediaRequest(tc.from, tc.role, `[{"type":"text","text":"keep"},`+tc.part+`]`), translator.NewRequestState())
			if err == nil || err.Class != tc.class || body != nil {
				t.Fatalf("body=%s error=%v, want %s", body, err, tc.class)
			}
		})
	}
}

func TestMediaNativeRequestsPreserveFields(t *testing.T) {
	for _, format := range formats {
		input := mediaRequest(format, "user", `[{"type":"vendor_media","data":"opaque"}]`)
		body, err := translator.ConvertRequest(format, "upstream", format, input, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, want := object(t, body), object(t, input)
		want["model"] = "upstream"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native media changed: %s", body)
		}
	}
}

func TestDocumentMetadataLossPolicy(t *testing.T) {
	input := mediaRequest(translator.FormatMessages, "user", `[{"type":"document","context":"context","citations":{"enabled":true},"source":{"type":"base64","data":"JVBERg==","media_type":"application/pdf"}}]`)
	for _, policy := range []translator.LossPolicy{translator.LossPolicyAllow, translator.LossPolicySafe, translator.LossPolicyStrict} {
		state := translator.NewRequestState()
		state.Options.LossPolicy = policy
		_, err := translator.ConvertRequest(translator.FormatResponses, "upstream", translator.FormatMessages, input, state)
		if (err == nil) != (policy == translator.LossPolicyAllow) {
			t.Fatalf("policy %s: %v", policy, err)
		}
		if ds := state.Diagnostics(); len(ds) != 2 || ds[0].Path != "messages[0].content[0].citations" || !ds[0].Semantic {
			t.Fatalf("diagnostics: %v", ds)
		}
	}
}

func TestResponsesToolMediaDoesNotDisappear(t *testing.T) {
	_, err := translator.ConvertRequest(translator.FormatChatCompletions, "upstream", translator.FormatResponses, []byte(`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_file","file_id":"file-1"}]}]}`), translator.NewRequestState())
	if err == nil || err.Class != translator.ClassUnsupported {
		t.Fatalf("tool file should be rejected: %v", err)
	}
}

func TestMixedMediaInputRoundTrip(t *testing.T) {
	input := mediaRequest(translator.FormatChatCompletions, "user", `[{"type":"text","text":"before"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERg=="}},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},{"type":"video_url","video_url":{"url":"https://example.test/a.mp4?x=1&y=2","processing":"agentic"}},{"type":"text","text":"after"}]`)
	state := translator.NewRequestState()
	state.Options.LossPolicy = translator.LossPolicyStrict
	responses, err := translator.ConvertRequest(translator.FormatResponses, "upstream", translator.FormatChatCompletions, input, state)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := translator.ConvertRequest(translator.FormatChatCompletions, "upstream", translator.FormatResponses, responses, state)
	if err != nil {
		t.Fatal(err)
	}
	want := object(t, input)["messages"].([]any)[0].(map[string]any)["content"]
	if got := mediaContent(t, translator.FormatChatCompletions, chat); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip content = %v, want %v", got, want)
	}
}

func TestMediaResponseRemainsUnsupported(t *testing.T) {
	_, err := translator.ConvertResponse(translator.FormatChatCompletions, translator.FormatMessages, 200, []byte(`{"choices":[{"message":{"role":"assistant","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg=="}}]},"finish_reason":"stop"}]}`), nil)
	if err == nil || err.Class != translator.ClassUnsupported {
		t.Fatalf("file output should be rejected: %v", err)
	}
}

func TestMessagesRejectsUnsupportedFileMIME(t *testing.T) {
	for _, from := range []string{translator.FormatChatCompletions, translator.FormatResponses} {
		for _, tc := range []struct {
			name, data string
			class      translator.Class
		}{
			{"missing MIME", "JVBERg==", translator.ClassUnsupported},
			{"octet stream", "data:application/octet-stream;base64,JVBERg==", translator.ClassUnsupported},
			{"image", "data:image/png;base64,JVBERg==", translator.ClassUnsupported},
			{"unknown MIME", "data:application/x-unknown;base64,JVBERg==", translator.ClassUnsupported},
			{"invalid text UTF-8", "data:text/plain;base64,/w==", translator.ClassTranslation},
			{"invalid text base64", "data:text/plain;base64,!", translator.ClassTranslation},
		} {
			t.Run(from+"/"+tc.name, func(t *testing.T) {
				part := map[string]any{"type": "input_file", "file_data": tc.data, "filename": "a.pdf"}
				if from == translator.FormatChatCompletions {
					part = map[string]any{"type": "file", "file": map[string]any{"file_data": tc.data, "filename": "a.pdf"}}
				}
				parts, err := json.Marshal([]any{part})
				if err != nil {
					t.Fatal(err)
				}
				body, convErr := translator.ConvertRequest(translator.FormatMessages, "upstream", from, mediaRequest(from, "user", string(parts)), translator.NewRequestState())
				if convErr == nil || convErr.Class != tc.class || body != nil {
					t.Fatalf("body=%s error=%v, want %s", body, convErr, tc.class)
				}
			})
		}
	}
}

func TestMessagesTextDocumentRoundTrip(t *testing.T) {
	input := mediaRequest(translator.FormatMessages, "user", `[{"type":"text","text":"before"},{"type":"document","title":"a.txt","source":{"type":"text","media_type":"text/plain","data":"hello\n世界\n"}},{"type":"text","text":"after"}]`)
	for _, intermediate := range []string{translator.FormatChatCompletions, translator.FormatResponses} {
		t.Run(intermediate, func(t *testing.T) {
			state := translator.NewRequestState()
			state.Options.LossPolicy = translator.LossPolicyStrict
			converted, err := translator.ConvertRequest(intermediate, "upstream", translator.FormatMessages, input, state)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := translator.ConvertRequest(translator.FormatMessages, "upstream", intermediate, converted, state)
			if err != nil {
				t.Fatal(err)
			}
			want := object(t, input)["messages"].([]any)[0].(map[string]any)["content"]
			if got := mediaContent(t, translator.FormatMessages, restored); !reflect.DeepEqual(got, want) {
				t.Fatalf("restored=%v, want %v", got, want)
			}
			if len(state.Diagnostics()) != 0 {
				t.Fatalf("unexpected losses: %v", state.Diagnostics())
			}
		})
	}
}

func TestMessagesToolResultsRejectDocuments(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"chat-tool", "function_call_output", "custom_tool_call_output"} {
			for _, file := range []string{
				`{"type":"input_file","file_data":"data:application/pdf;base64,JVBERg=="}`,
				`{"type":"input_file","file_data":"data:text/plain;base64,aGVsbG8="}`,
				`{"type":"input_file","file_url":"https://example.test/a.pdf"}`,
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", kind, stream, file), func(t *testing.T) {
					from := translator.FormatResponses
					parts := `[{"type":"text","text":"before"},{"type":"input_image","image_url":"https://example.test/a.png"},` + file + `,{"type":"text","text":"after"}]`
					payload := fmt.Sprintf(`{"stream":%t,"input":[{"type":%q,"call_id":"c1","output":%s}]}`, stream, kind, parts)
					if kind == "chat-tool" {
						from = translator.FormatChatCompletions
						var part map[string]any
						json.Unmarshal([]byte(file), &part)
						delete(part, "type")
						chatFile, _ := json.Marshal(map[string]any{"type": "file", "file": part})
						parts = `[{"type":"text","text":"before"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}},` + string(chatFile) + `,{"type":"text","text":"after"}]`
						payload = fmt.Sprintf(`{"stream":%t,"messages":[{"role":"tool","tool_call_id":"c1","content":%s}]}`, stream, parts)
					}
					body, err := translator.ConvertRequest(translator.FormatMessages, "upstream", from, []byte(payload), translator.NewRequestState())
					if err == nil || err.Class != translator.ClassUnsupported || body != nil {
						t.Fatalf("body=%s, error=%v", body, err)
					}
				})
			}
		}
	}
}

func TestNullDocumentMetadataHasNoLoss(t *testing.T) {
	input := mediaRequest(translator.FormatMessages, "user", `[{"type":"document","context":null,"citations":null,"source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}}]`)
	for _, to := range []string{translator.FormatChatCompletions, translator.FormatResponses} {
		for _, policy := range []translator.LossPolicy{translator.LossPolicyAllow, translator.LossPolicySafe, translator.LossPolicyStrict} {
			state := translator.NewRequestState()
			state.Options.LossPolicy = policy
			if _, err := translator.ConvertRequest(to, "upstream", translator.FormatMessages, input, state); err != nil {
				t.Fatalf("%s/%s: %v", to, policy, err)
			}
			if ds := state.Diagnostics(); len(ds) != 0 {
				t.Fatalf("%s/%s: %v", to, policy, ds)
			}
		}
	}
}
