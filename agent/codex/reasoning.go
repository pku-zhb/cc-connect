package codex

import (
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Existing sessions read the workspace's current selection for each new turn.
// An unset selection keeps the native session's reported default.
func selectedReasoningEffort(source func() string, fallback string) string {
	if source != nil {
		if effort := normalizeReasoningEffort(source()); effort != "" {
			return effort
		}
	}
	return normalizeRuntimeReasoningEffort(fallback)
}

func (*Agent) SupportsLiveReasoningEffort() bool { return true }

func preserveResumeError(sessionID string, err error) error {
	if sessionID == "" || sessionID == core.ContinueSession || err == nil {
		return err
	}
	return &core.SessionResumeError{
		Cause: err,
		InUse: strings.Contains(strings.ToLower(err.Error()), "already has an active writer"),
	}
}
