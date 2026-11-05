package core

import (
	"context"
	"strings"
	"time"
)

// Fast uses the same normalized workspace key as model/reasoning overrides.
// Sessions retain their native ID and read the workspace selection per turn.
func (e *Engine) restoreWorkspaceServiceTier(agent Agent, workspace string) {
	if e.projectState == nil || workspace == "" {
		return
	}
	if tier := e.projectState.WorkspaceServiceTierOverride(workspace); tier != "" {
		if switcher, ok := agent.(ServiceTierSwitcher); ok {
			switcher.SetServiceTier(tier)
		}
	}
}

func (e *Engine) cmdFast(p Platform, msg *Message, args []string) {
	agent, _, key, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	switcher, ok := agent.(ServiceTierSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastNotSupported))
		return
	}
	action := "toggle"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	if len(args) > 1 || (action != "toggle" && action != "on" && action != "off" && action != "status") {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastUsage))
		return
	}
	workspace := workspaceModelOverrideKey(key, msg.SessionKey, agent)
	e.restoreWorkspaceServiceTier(agent, workspace)
	tier := "priority"
	if action == "off" {
		tier = "default"
	}
	if action == "toggle" || action == "status" {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		tier, err = e.workspaceServiceTier(ctx, workspace, key, switcher)
		cancel()
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastReadFailed, err))
			return
		}
		if action == "status" {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastStatus, fastModeLabel(tier)))
			return
		}
		if tier == "priority" || tier == "fast" {
			tier = "default"
		} else {
			tier = "priority"
		}
	}
	switcher.SetServiceTier(tier)
	if e.projectState != nil && workspace != "" {
		e.projectState.SetWorkspaceServiceTierOverride(workspace, tier)
		e.projectState.Save()
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastChanged, fastModeLabel(tier)))
}

func (e *Engine) workspaceServiceTier(ctx context.Context, workspace, key string, switcher ServiceTierSwitcher) (string, error) {
	// An explicit workspace choice takes priority over a thread's old setting.
	if e.projectState != nil {
		if tier := e.projectState.WorkspaceServiceTierOverride(workspace); tier != "" {
			return tier, nil
		}
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state != nil {
		state.mu.Lock()
		live := state.agentSession
		state.mu.Unlock()
		if reporter, ok := live.(ServiceTierReporter); ok {
			return reporter.GetServiceTier(ctx)
		}
	}
	return switcher.GetServiceTier(ctx)
}

func fastModeLabel(tier string) string {
	if tier == "priority" || tier == "fast" {
		return "on"
	}
	return "off"
}
