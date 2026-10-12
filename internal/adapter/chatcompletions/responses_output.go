package chatcompletions

import (
	"strings"

	"github.com/dillonzq/api-translator/internal/adapter/shared"
)

// responsesOutput owns synthesized output items independently of Messages blocks.
type responsesOutput struct {
	nextIndex    int
	msgIndex     int
	reasonIndex  int
	reasoning    strings.Builder
	text         strings.Builder
	refusal      strings.Builder
	refusalFirst bool
	citations    []shared.Citation
	citationSent int
}
