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
	ctx := toolState(state)
	if sourceFormat == FormatResponses {
		ctx = ctx.Clone()
		var err *Error
		payload, err = shared.NormalizeShellRequest(payload, ctx)
		if err != nil {
			return nil, err
		}
	}
	var body []byte
	var err *Error
	switch targetFormat {
	case FormatChatCompletions:
		body, err = chatcompletions.BuildRequest(upstreamModel, sourceFormat, payload, ctx)
	case FormatMessages:
		body, err = messages.BuildRequest(upstreamModel, sourceFormat, payload, ctx)
	default:
		body, err = responses.BuildRequest(upstreamModel, sourceFormat, payload, ctx)
	}
	if sourceFormat == FormatResponses {
		if err == nil {
			state.tools = *ctx
		} else {
			for _, diagnostic := range ctx.Diagnostics() {
				state.tools.Record(diagnostic)
			}
		}
	}
	return body, err
}
