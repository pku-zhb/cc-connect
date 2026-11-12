package core

// applyReasoningEffortChange keeps live-capable agents attached to their native
// conversation. Other agents retain their existing session-reset behavior.
func (e *Engine) applyReasoningEffortChange(agent Agent, sessions *SessionManager, interactiveKey, sessionKey, effort string) bool {
	switcher := agent.(ReasoningEffortSwitcher)
	switcher.SetReasoningEffort(effort)
	e.persistWorkspaceEffortOverride(interactiveKey, sessionKey, agent, switcher.GetReasoningEffort())
	if live, ok := agent.(LiveReasoningEffortSwitcher); ok && live.SupportsLiveReasoningEffort() {
		return true
	}
	e.cleanupInteractiveState(interactiveKey)
	s := sessions.GetOrCreateActive(sessionKey)
	s.SetAgentSessionID("", "")
	s.ClearHistory()
	sessions.Save()
	return false
}
