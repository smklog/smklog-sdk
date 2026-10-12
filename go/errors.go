package smklog

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors for errors.Is. Every *APIError reports the one that matches
// its Code, so the four Is* helpers below are the same test spelled shorter.
var (
	// ErrRateLimited: a 429 (rate_limited, slow_down). The hourly bucket is spent;
	// the client never retries this, the caller decides when to come back.
	ErrRateLimited = errors.New("smklog: rate limited")
	// ErrInvalidRequest: the request cannot be served as written (missing fields,
	// bad JSON, an item or URL the estimator could not recognize), or this client
	// refused it locally before spending a request (Status 0).
	ErrInvalidRequest = errors.New("smklog: invalid request")
	// ErrNotFound: a 404, no payment session with that id (sessions answer for 30 days).
	ErrNotFound = errors.New("smklog: session not found")
	// ErrSessionFailed: a 422, the shipment cannot become a checkout (past parcel
	// limits, restricted, more than one box, or unpriceable).
	ErrSessionFailed = errors.New("smklog: session failed")
	// ErrNetwork: no HTTP answer arrived (connection failure or timeout). The
	// transport's error is reachable through errors.Unwrap / errors.As.
	ErrNetwork = errors.New("smklog: network error")
)

// APIError is every failure the client returns: an API refusal, a transport
// failure, or a request rejected locally. Status is the HTTP status, 0 when no
// HTTP exchange happened. Code is the stable code from the Problem body
// (rate_limited, session_not_found, ...), network_error when no answer came,
// or the API's own code for a local refusal so one check handles both.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Missing lists the field names the API (or the local validation) still
	// needs; often empty.
	Missing []string
	// Body is the response body as received, nil when there was none.
	Body []byte
	// RetryAfter is the Retry-After header of a 429 when the API sent one,
	// 0 otherwise. The client never acts on it; it is for the caller.
	RetryAfter time.Duration
	// Err is the transport error behind a network_error, nil otherwise.
	Err error
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("smklog: ")
	b.WriteString(e.Code)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if len(e.Missing) > 0 {
		fmt.Fprintf(&b, " [missing: %s]", strings.Join(e.Missing, ", "))
	}
	return b.String()
}

// Unwrap exposes the transport error so errors.Is(err, context.DeadlineExceeded)
// and friends keep working through the wrapper.
func (e *APIError) Unwrap() error { return e.Err }

// Is matches the sentinel that classifies this error's Code.
func (e *APIError) Is(target error) bool { return target != nil && target == sentinelFor(e.Code) }

// The classification shared by every SMKlog SDK (docs/DESIGN.md, "Errors").
func sentinelFor(code string) error {
	switch code {
	case "rate_limited", "slow_down":
		return ErrRateLimited
	case "product_not_recognized", "product_url_not_detected", "missing_required_fields", "invalid_request", "invalid_json":
		return ErrInvalidRequest
	case "session_not_found":
		return ErrNotFound
	case "session_failed":
		return ErrSessionFailed
	case "network_error":
		return ErrNetwork
	}
	return nil
}

// IsRateLimited reports whether err is a 429 (rate_limited or slow_down).
func IsRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }

// IsInvalidRequest reports whether err is a request the API, or this client
// locally, refused as malformed or unrecognizable.
func IsInvalidRequest(err error) bool { return errors.Is(err, ErrInvalidRequest) }

// IsNotFound reports whether err is a 404 for an unknown or expired session.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsSessionFailed reports whether err is a 422 from CreatePaymentSession.
func IsSessionFailed(err error) bool { return errors.Is(err, ErrSessionFailed) }

// A local refusal reuses the API's own codes with Status 0, so a caller's
// single errors.Is check covers both the server's and the client's verdict.
func invalid(message string, missing ...string) *APIError {
	code := "invalid_request"
	if len(missing) > 0 {
		code = "missing_required_fields"
	}
	return &APIError{Code: code, Message: message, Missing: missing}
}
