package translator

import (
	"github.com/dillonzq/api-translator/internal/errclass"
)

// Error carries the error class, HTTP status, and retryability.
type Error = errclass.Error

// Class is a stable error-category identifier.
type Class = errclass.Class

const (
	ClassAuth         = errclass.ClassAuth
	ClassInvalidModel = errclass.ClassInvalidModel
	ClassUnsupported  = errclass.ClassUnsupported
	ClassRateLimit    = errclass.ClassRateLimit
	ClassQuota        = errclass.ClassQuota
	ClassBilling      = errclass.ClassBilling
	ClassUpstream     = errclass.ClassUpstream
	ClassNetwork      = errclass.ClassNetwork
	ClassTranslation  = errclass.ClassTranslation
)

func Translation(message string) *Error            { return errclass.Translation(message) }
func FromStatus(status int, message string) *Error { return errclass.FromStatus(status, message) }
func FromNetwork(err error) *Error                 { return errclass.FromNetwork(err) }
func PostFirstByteNetwork(message string) *Error   { return errclass.PostFirstByteNetwork(message) }
func UpstreamFallback(message string) *Error       { return errclass.UpstreamFallback(message) }
func Redact(message string) string                 { return errclass.Redact(message) }
