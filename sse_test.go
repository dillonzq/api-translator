package translator

import (
	"reflect"
	"testing"
)

func TestSSEDecoderChunkBoundaries(t *testing.T) {
	wire := []byte(": heartbeat\r\nevent: chunk\r\ndata: {\r\ndata: \"x\":1}\r\n\r\ndata: [DONE]\r\n\r\n")
	want := []SSEEvent{{Event: "chunk", Data: []byte("{\n\"x\":1}")}, {Data: []byte("[DONE]"), Done: true}}
	for split := 0; split <= len(wire); split++ {
		var decoder SSEDecoder
		got := decoder.Feed(wire[:split])
		got = append(got, decoder.Feed(wire[split:])...)
		got = append(got, decoder.Finish()...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split %d: %+v", split, got)
		}
		if len(decoder.Finish()) != 0 || len(decoder.Feed(wire)) != 0 {
			t.Fatal("closed decoder emitted events")
		}
	}
}

func TestSSEDecoderHostFraming(t *testing.T) {
	stream, err := NewStreamConverter(FormatChatCompletions, FormatChatCompletions, nil)
	if err != nil {
		t.Fatal(err)
	}
	frames, done, err := stream.Feed([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	var decoder SSEDecoder
	var payloads [][]byte
	markers := 0
	for _, frame := range frames {
		for _, event := range decoder.Feed(frame) {
			if event.Done {
				markers++
				continue
			}
			payloads = append(payloads, event.Data)
		}
	}
	if markers != 1 || len(payloads) != 1 || payloads[0][0] != '{' {
		t.Fatalf("markers=%d payloads=%q", markers, payloads)
	}
	var eof SSEDecoder
	if len(eof.Feed([]byte("data: tail"))) != 0 {
		t.Fatal("premature frame")
	}
	if got := eof.Finish(); len(got) != 1 || string(got[0].Data) != "tail" {
		t.Fatalf("EOF=%v", got)
	}
}

func TestSSEDecoderEmptyData(t *testing.T) {
	for _, wire := range []string{"data:\n\n", "data: \r\n\r\n", "data:\r\r", "data:"} {
		for split := 0; split <= len(wire); split++ {
			var decoder SSEDecoder
			got := decoder.Feed([]byte(wire[:split]))
			got = append(got, decoder.Feed([]byte(wire[split:]))...)
			got = append(got, decoder.Finish()...)
			if len(got) != 1 || len(got[0].Data) != 0 || got[0].Done || got[0].Event != "" {
				t.Fatalf("wire=%q split=%d events=%+v", wire, split, got)
			}
		}
	}
	var decoder SSEDecoder
	got := decoder.Feed([]byte(": comment\n\n\n\nevent: ping\ndata:\n\n"))
	if len(got) != 1 || got[0].Event != "ping" || len(got[0].Data) != 0 {
		t.Fatalf("events=%+v", got)
	}
}
