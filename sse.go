package translator

import "github.com/dillonzq/api-translator/internal/adapter/shared"

// SSEEvent is an unframed SSE event. Data joins multiple data lines with LF.
// Done identifies the Chat Completions [DONE] marker, not protocol completion
// for Responses or Messages. Event is the optional event field.
type SSEEvent struct {
	Event string
	Data  []byte
	Done  bool
}

// SSEDecoder removes SSE framing without interpreting JSON. Its zero value is
// ready to use. Use one decoder per stream, sequentially; call Finish at EOF.
// This lets hosts that add their own data: framing consume converter output.
type SSEDecoder struct {
	framer *shared.SSEFramer
	closed bool
}

func (d *SSEDecoder) drain() []SSEEvent {
	var events []SSEEvent
	for {
		name, data, _, ok := d.framer.Next()
		if !ok {
			return events
		}
		events = append(events, SSEEvent{Event: name, Data: []byte(data), Done: shared.IsSSEDone(data)})
	}
}

// Feed accepts arbitrary chunks, including complete StreamConverter frames.
// Comments are ignored. Calls after Finish return no events.
func (d *SSEDecoder) Feed(chunk []byte) []SSEEvent {
	if d.closed {
		return nil
	}
	if d.framer == nil {
		d.framer = shared.NewSSEFramer(false)
	}
	d.framer.Push(chunk)
	return d.drain()
}

// Finish flushes an event missing its final line separator and closes decoding.
// It does not validate protocol completion; StreamConverter.Finish does that.
func (d *SSEDecoder) Finish() []SSEEvent {
	if d.closed {
		return nil
	}
	d.closed = true
	if d.framer == nil {
		return nil
	}
	d.framer.End()
	return d.drain()
}
