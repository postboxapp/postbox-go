package postbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Error is the single typed error the SDK returns for any non-2xx response.
// The (Status, Code) pair is stable across every Postbox SDK: a 409 is a
// conflict everywhere. RequestID is the string a customer quotes in a ticket.
type Error struct {
	Status  int
	Code    string
	Message string
	// RequestID is populated from the X-Request-Id response header.
	RequestID string
	// RetryAfter is the Retry-After value in seconds, set only on 429s that
	// carried the header.
	RetryAfter int
}

func (e *Error) Error() string {
	parts := []string{fmt.Sprintf("postbox: HTTP %d", e.Status)}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	msg := strings.Join(parts, ": ")
	if e.RequestID != "" {
		msg += fmt.Sprintf(" (request id %s)", e.RequestID)
	}
	return msg
}

// errorFromResponse maps an HTTP response into a typed *Error.
func errorFromResponse(status int, body []byte, requestID, retryAfter string) *Error {
	var env struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &env)

	msg := env.Error
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	e := &Error{Status: status, Code: env.Code, Message: msg, RequestID: requestID}
	if status == 429 && retryAfter != "" {
		if sec, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil {
			e.RetryAfter = sec
		}
	}
	return e
}

// asError extracts a *Error from err, if present.
func asError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsRateLimit reports whether err is a 429 rate-limit error.
func IsRateLimit(err error) bool {
	e, ok := asError(err)
	return ok && e.Status == 429
}

// IsNotFound reports whether err is a 404 not-found error.
func IsNotFound(err error) bool {
	e, ok := asError(err)
	return ok && e.Status == 404
}

// IsValidation reports whether err is a 400/422 validation error.
func IsValidation(err error) bool {
	e, ok := asError(err)
	return ok && (e.Status == 400 || e.Status == 422)
}

// IsAuth reports whether err is a 401/403 authentication or permission error.
func IsAuth(err error) bool {
	e, ok := asError(err)
	return ok && (e.Status == 401 || e.Status == 403)
}
