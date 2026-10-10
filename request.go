package translator

import (
	"github.com/dillonzq/api-translator/internal/adapter/chatcompletions"
	"github.com/dillonzq/api-translator/internal/adapter/messages"
	"github.com/dillonzq/api-translator/internal/adapter/responses"
	"github.com/dillonzq/api-translator/internal/adapter/shared"
)

// ConvertRequest converts a request directly and applies the execution loss policy.
func ConvertRequest(targetFormat, upstreamModel, sourceFormat string, payload []byte, state *RequestState) ([]byte, *Error) {
	if err := validateFormats(targetFormat, sourceFormat); err != nil {
		return nil, err
	}
	if err := toolState(state).CheckRequestLosses(sourceFormat, targetFormat, payload); err != nil {
		return nil, err
	}
	if sourceFormat == targetFormat {
		return shared.RewriteModelID(upstreamModel, payload, sourceFormat)
	}
	if sourceFormat == FormatResponses && state == nil {
		return nil, Translation("Responses cross-protocol conversion requires request state")
	}
	switch targetFormat {
	case FormatChatCompletions:
		return chatcompletions.BuildRequest(upstreamModel, sourceFormat, payload, toolState(state))
	case FormatMessages:
		return messages.BuildRequest(upstreamModel, sourceFormat, payload, toolState(state))
	default:
		return responses.BuildRequest(upstreamModel, sourceFormat, payload, toolState(state))
	}
}
