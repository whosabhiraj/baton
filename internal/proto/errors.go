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
	CodeUnknownQID       = "UNKNOWN_QID" // answer for a qid that is not an open question of the run

	// Spawn / account selection (spec §4.2; checked in this order).
	CodeAccountRequired = "ACCOUNT_REQUIRED"  // no explicit account and no routing[role]
	CodeUnknownAccount  = "UNKNOWN_ACCOUNT"   // account id not in config
	CodeAccountDisabled = "ACCOUNT_DISABLED"  // account.enabled = false
	CodeRoleNotAllowed  = "ROLE_NOT_ALLOWED"  // role not in account.roles
	CodeModelNotAllowed = "MODEL_NOT_ALLOWED" // model not in account.models
	CodeAccountBusy     = "ACCOUNT_BUSY"      // account at max_concurrent
	CodeMaxAgents       = "MAX_AGENTS"        // global cap (auto mode only)
	CodeCallsignTaken   = "CALLSIGN_TAKEN"    // custom callsign used by a live agent
	CodeNotARepo        = "NOT_A_REPO"        // target folder is not a git repository
	CodeGit             = "GIT_ERROR"         // a git command failed
	CodeConfigInvalid   = "CONFIG_INVALID"    // config write rejected by validation

	// Phase 3 (A15).
	CodeAlreadyDecided = "ALREADY_DECIDED" // approval already approved/rejected
	CodeNotSupported   = "NOT_SUPPORTED"   // no launcher (kill)
	CodeLaunchFailed   = "LAUNCH_FAILED"   // launcher could not start the agent
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
	case CodeUnknownQID, CodeAccountDisabled, CodeRoleNotAllowed, CodeModelNotAllowed,
		CodeAccountBusy, CodeMaxAgents, CodeNotARepo, CodeCallsignTaken, CodeAlreadyDecided, CodeNotSupported:
		return http.StatusConflict
	case CodeAccountRequired, CodeUnknownAccount, CodeConfigInvalid:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// NewError builds an Error for code with the standard HTTP status for that code.
func NewError(code, msg string) *Error {
	return &Error{Status: statusFor(code), Code: code, Msg: msg}
}
