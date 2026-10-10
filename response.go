package translator

import (
	"github.com/dillonzq/api-translator/internal/adapter/chatcompletions"
	"github.com/dillonzq/api-translator/internal/adapter/messages"
	"github.com/dillonzq/api-translator/internal/adapter/responses"
)

// ConvertResponse converts an upstream response to the client protocol.
func ConvertResponse(upstreamFormat, outputFormat string, status int, body []byte, state *RequestState) ([]byte, *Error) {
	if err := validateFormats(upstreamFormat, outputFormat); err != nil {
		return nil, err
	}
	switch upstreamFormat {
	case FormatChatCompletions:
		return chatcompletions.ConvertNonStreamResponse(outputFormat, status, body, toolState(state))
	case FormatMessages:
		return messages.ConvertNonStreamResponse(outputFormat, status, body, toolState(state))
	default:
		return responses.ConvertNonStreamResponse(outputFormat, status, body, toolState(state))
	}
}
