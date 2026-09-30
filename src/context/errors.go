package context

import (
	"errors"
	"fmt"
)

// ErrorCode is the machine-readable reason a Context Service call failed. The
// codes are the ones an Agent needs to tell retry / change query / ask human /
// continue-without-resource apart; a blanket 500 is forbidden (spec 18).
type ErrorCode string

const (
	// ErrResourceNotFound: the named resource or section does not exist.
	ErrResourceNotFound ErrorCode = "RESOURCE_NOT_FOUND"
	// ErrProjectNotFound: the request did not name a usable project scope.
	ErrProjectNotFound ErrorCode = "PROJECT_NOT_FOUND"
	// ErrSourceUnavailable: the source could not be read (missing/unreadable).
	ErrSourceUnavailable ErrorCode = "SOURCE_UNAVAILABLE"
	// ErrParseFailed: the source was read but could not be parsed.
	ErrParseFailed ErrorCode = "PARSE_FAILED"
	// ErrIndexFailed: persistence/indexing the parsed sections failed.
	ErrIndexFailed ErrorCode = "INDEX_FAILED"
	// ErrInvalidQuery: the request itself is malformed (empty query, bad type).
	ErrInvalidQuery ErrorCode = "INVALID_QUERY"
)

// Error is a typed Context Service failure. Callers use Code (or IsCode) to
// decide what to do; they never branch on the message.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a typed error with a formatted message.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a typed code to an underlying error.
func Wrap(code ErrorCode, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf reports the ErrorCode of err, or "" when err carries none.
func CodeOf(err error) ErrorCode {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// IsCode reports whether err is a Context Service error with this code.
func IsCode(err error, code ErrorCode) bool { return CodeOf(err) == code }
