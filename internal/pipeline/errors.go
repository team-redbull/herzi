package pipeline

import "errors"

// ErrAmbiguous means the install outcome is unknown; callers must block the pool.
var ErrAmbiguous = errors.New("install outcome is ambiguous")

// ErrRejected means the API refused the request and nothing started.
var ErrRejected = errors.New("install request rejected")

// Failure kinds of an install request; Kind is stable and safe to put in a log field.
const (
	KindTimeout            = "timeout"
	KindCanceled           = "canceled"
	KindDNS                = "dns_failure"
	KindConnectionRefused  = "connection_refused"
	KindConnectionReset    = "connection_reset"
	KindConnectionClosed   = "server_closed_connection"
	KindUnreachable        = "network_unreachable"
	KindTLSUntrusted       = "tls_certificate_not_trusted"
	KindTLSHostname        = "tls_hostname_mismatch"
	KindTLSInvalid         = "tls_certificate_invalid"
	KindTLSNotTLS          = "server_is_not_tls"
	KindTLSOther           = "tls_error"
	KindTransport          = "transport_error"
	KindRejected           = "rejected_by_api"
	KindUnexpectedStatus   = "unexpected_http_status"
	KindInvalidResponse    = "invalid_response"
	KindResponseUnreadable = "response_unreadable"
	KindBadRequest         = "invalid_request"
)

// InstallError says why an install request failed, with text that is safe to log (no token, no query string).
// It unwraps to ErrAmbiguous or ErrRejected, and to the context error for a timeout or cancellation.
type InstallError struct {
	Kind   string
	Class  error
	HTTP   int
	Detail string
	Body   string
	URL    string
	Cause  error
}

func (e *InstallError) Error() string { return e.Class.Error() + ": " + e.Detail }

func (e *InstallError) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Class, e.Cause}
	}
	return []error{e.Class}
}
