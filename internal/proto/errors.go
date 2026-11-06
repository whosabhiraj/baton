package proto

import "net/http"

// Error codes returned by the hub API and by validation (PROTOCOL.md A6, A11).
const (
	CodeInvalidEnvelope  = "INVALID_ENVELOPE"
	CodeInvalidPayload   = "INVALID_PAYLOAD"
	CodeHubOnlyType      = "HUB_ONLY_TYPE"
	CodeUnknownInboxType = "UNKNOWN_INBOX_TYPE"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeRunMismatch      = "RUN_MISMATCH"
	CodeAgentMismatch    = "AGENT_MISMATCH"
	CodeRunEnded         = "RUN_ENDED"
	CodeNotFound         = "NOT_FOUND"
	CodeInternal         = "INTERNAL"
)

// Error is a protocol error carrying the HTTP status the API should answer with.
type Error struct {
	Status int
	Code   string
	Msg    string
}

// Error implements the error interface as "CODE: message".
func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

// statusFor maps an error code to its HTTP status (PROTOCOL.md A11).
func statusFor(code string) int {
	switch code {
	case CodeInvalidEnvelope, CodeInvalidPayload, CodeHubOnlyType, CodeUnknownInboxType:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeRunMismatch, CodeAgentMismatch:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeRunEnded:
		return http.StatusGone
	default:
		return http.StatusInternalServerError
	}
}

// NewError builds an Error for code with the standard HTTP status for that code.
func NewError(code, msg string) *Error {
	return &Error{Status: statusFor(code), Code: code, Msg: msg}
}
