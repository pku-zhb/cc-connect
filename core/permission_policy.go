package core

import "log/slog"

// Reconcile an explicit live policy switch with an already displayed approval.
// User questions remain pending; changing policy never supplies their answers.
func (e *Engine) reconcilePermissionPolicy(key string, live AgentSession) {
	policy, supported := live.(PermissionPolicyReporter)
	if !supported {
		return
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.approveAll = false // the newly selected native policy is authoritative
	pending := state.pending
	state.mu.Unlock()
	if !policy.AutoApprovesPermissions() || pending == nil || len(pending.Questions) != 0 {
		return
	}
	if err := live.RespondPermission(pending.RequestID, PermissionResult{
		Behavior: "allow", UpdatedInput: pending.ToolInput,
	}); err != nil {
		slog.Warn("permission policy: could not resolve existing approval", "error", err)
		return
	}
	state.mu.Lock()
	if state.pending == pending {
		state.pending = nil
	}
	state.mu.Unlock()
	pending.resolve()
}
