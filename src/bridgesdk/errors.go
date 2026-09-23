package bridgesdk

import "fmt"

// BridgeProcessError reports a failure to spawn or handshake with the bridge.
type BridgeProcessError struct{ Msg string }

func (e *BridgeProcessError) Error() string { return "bridge: " + e.Msg }

// RPCError is an error returned by the bridge for one command.
type RPCError struct {
	Code    string
	Message string
}

func (e *RPCError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// RunError is a failed run (provider error, aborted run, ...).
type RunError struct {
	Code    string
	Message string
}

func (e *RunError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("run %s: %s", e.Code, e.Message)
}

func bridgeErr(format string, args ...any) error {
	return &BridgeProcessError{Msg: fmt.Sprintf(format, args...)}
}
