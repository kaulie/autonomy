package cursorsdk

import (
	"fmt"
	"time"
)

// CursorSdkError is the base error for this adapter.
type CursorSdkError struct {
	Msg string
}

func (e *CursorSdkError) Error() string { return e.Msg }

func sdkErr(msg string) error { return &CursorSdkError{Msg: msg} }

// BridgeProcessError means the bridge failed to launch, handshake, or shut down.
type BridgeProcessError struct {
	Msg string
}

func (e *BridgeProcessError) Error() string { return e.Msg }

// TransportError means the HTTP connection to the bridge failed.
type TransportError struct {
	Msg string
}

func (e *TransportError) Error() string { return e.Msg }

// RateLimitInfo is optional rate-limit metadata from a failed RPC.
type RateLimitInfo struct {
	Limit             *int64
	Remaining         *int64
	ResetEpochSeconds *int64
}

// RpcError is a failed Connect RPC with structured sdk.v1 details when present.
type RpcError struct {
	Code         string
	Message      string
	SdkErrorCode string
	RequestID    string
	HelpURL      string
	Provider     string
	RetryAfter   *time.Duration
	RateLimit    *RateLimitInfo
}

func (e *RpcError) Error() string {
	text := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.SdkErrorCode != "" && e.SdkErrorCode != "UNSPECIFIED" {
		text += fmt.Sprintf(" [%s]", e.SdkErrorCode)
	}
	if e.RequestID != "" {
		text += fmt.Sprintf(" (request_id=%s)", e.RequestID)
	}
	return text
}

type AuthenticationError struct{ RpcError }
type PermissionDeniedError struct{ RpcError }
type NotFoundError struct{ RpcError }
type ValidationError struct{ RpcError }
type RateLimitError struct{ RpcError }
type AgentBusyError struct{ RpcError }
type InvalidStateError struct{ RpcError }

func mapRpcError(code, message, sdkCode, requestID string) error {
	base := RpcError{
		Code:         code,
		Message:      message,
		SdkErrorCode: sdkCode,
		RequestID:    requestID,
	}
	switch sdkCode {
	case "AUTHENTICATION", "UNAUTHENTICATED":
		return &AuthenticationError{RpcError: base}
	case "PERMISSION_DENIED":
		return &PermissionDeniedError{RpcError: base}
	case "NOT_FOUND":
		return &NotFoundError{RpcError: base}
	case "VALIDATION", "INVALID_ARGUMENT":
		return &ValidationError{RpcError: base}
	case "RATE_LIMIT":
		return &RateLimitError{RpcError: base}
	case "AGENT_BUSY":
		return &AgentBusyError{RpcError: base}
	case "INVALID_STATE":
		return &InvalidStateError{RpcError: base}
	}
	switch code {
	case "unauthenticated":
		return &AuthenticationError{RpcError: base}
	case "permission_denied":
		return &PermissionDeniedError{RpcError: base}
	case "not_found":
		return &NotFoundError{RpcError: base}
	case "invalid_argument":
		return &ValidationError{RpcError: base}
	case "resource_exhausted":
		return &RateLimitError{RpcError: base}
	}
	return &base
}
