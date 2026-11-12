package codex

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
