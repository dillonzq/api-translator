// Package translator provides request, response, and SSE protocol conversion
// between Chat Completions, Responses, and Messages. HTTP transport and
// provider policy belong to callers.
package translator

import (
	"github.com/dillonzq/api-translator/internal/adapter/shared"
	"github.com/dillonzq/api-translator/internal/errclass"
)

// Protocol identifiers are independent of provider and host format names.
// Callers map external identifiers to these values.
const (
	FormatChatCompletions = shared.FormatChatCompletions
	FormatResponses       = shared.FormatResponses
	FormatMessages        = shared.FormatMessages
)

// RequestState holds tool identities, options, and diagnostics for one execution. Its zero value is ready
// to use. Pass the same state through request and response conversion. Do not
// reuse it across executions or mutate it concurrently with response conversion.
type RequestState struct {
	Options ConversionOptions
	tools   shared.ResponseTools
}

func NewRequestState() *RequestState { return &RequestState{} }

func toolState(state *RequestState) *shared.ResponseTools {
	if state == nil {
		return nil
	}
	state.tools.Options = state.Options
	return &state.tools
}

func validateFormats(formats ...string) *Error {
	for _, format := range formats {
		switch format {
		case FormatChatCompletions, FormatResponses, FormatMessages:
		default:
			return &Error{Class: ClassUnsupported, Message: errclass.Redact("unsupported protocol format: " + format)}
		}
	}
	return nil
}
