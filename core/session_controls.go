package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionControlProvider opts an agent into controls on its live session.
// The concrete session advertises the options and data it actually supports.
type SessionControlProvider interface {
	UsesSessionControls() bool
}

type SessionConfigValue struct {
	Value, Name, Description string
}

type SessionConfigOption struct {
	ID, Name, Description, Category, CurrentValue string
	Options                                       []SessionConfigValue
}

type SessionConfigurer interface {
	ConfigOptions() []SessionConfigOption
	SetConfigOption(context.Context, string, string) error
}

// SessionUsage contains reported session data, independently of account quotas.
// Pointers distinguish an unreported value from a reported zero.
type SessionUsage struct {
	Used, Size *int64
	Cost       *SessionCost
}

type SessionCost struct {
	Amount   float64
	Currency string
}

type SessionUsageReporter interface {
	GetSessionUsage() *SessionUsage
}

func usesSessionControls(agent Agent) bool {
	p, ok := agent.(SessionControlProvider)
	return ok && p.UsesSessionControls()
}

func (e *Engine) sessionForControl(p Platform, msg *Message, agent Agent, sessions *SessionManager, key string) AgentSession {
	session := sessions.GetOrCreateActive(msg.SessionKey)
	state := e.getOrCreateInteractiveStateWith(key, p, msg.ReplyCtx, session, sessions, agent, msg.SessionKey)
	state.mu.Lock()
	live := state.agentSession
	state.mu.Unlock()
	if live == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFailedToStartAgentSession))
	} else if err := e.applyWorkspaceSessionConfig(live, agent, key, msg.SessionKey); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChangeFailed, err))
		return nil
	}
	return live
}

func sessionConfigForCategory(options []SessionConfigOption, category string) *SessionConfigOption {
	for i := range options {
		if options[i].Category == category {
			return &options[i]
		}
	}
	// Categories are optional ACP metadata. Recognize conventional IDs too.
	for i := range options {
		id := options[i].ID
		if id == category || (category == "thought_level" && (id == "reasoning_effort" || id == "reasoning")) {
			return &options[i]
		}
	}
	return nil
}

func (e *Engine) sessionConfigCommand(p Platform, msg *Message, agent Agent, sessions *SessionManager, key, category string, args []string) {
	live := e.sessionForControl(p, msg, agent, sessions, key)
	if live == nil {
		return
	}
	cfg, ok := live.(SessionConfigurer)
	var option *SessionConfigOption
	if ok {
		option = sessionConfigForCategory(cfg.ConfigOptions(), category)
	}
	if option == nil {
		if category == "model" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelNotSupported))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgReasoningNotSupported))
		}
		return
	}
	if len(args) == 0 {
		var sb strings.Builder
		if category == "model" {
			sb.WriteString(e.i18n.Tf(MsgModelCurrent, option.CurrentValue))
			sb.WriteString("\n\n" + e.i18n.T(MsgModelListTitle))
		} else {
			sb.WriteString(e.i18n.Tf(MsgReasoningCurrent, option.CurrentValue))
			sb.WriteString("\n\n" + e.i18n.T(MsgReasoningListTitle))
		}
		for i, value := range option.Options {
			marker := "  "
			if value.Value == option.CurrentValue {
				marker = "> "
			}
			label := value.Value
			if value.Name != "" && value.Name != value.Value {
				label += " — " + value.Name
			}
			fmt.Fprintf(&sb, "%s%d. %s\n", marker, i+1, label)
		}
		if category == "model" {
			sb.WriteString("\n" + e.i18n.T(MsgModelUsage))
		} else {
			var values []string
			for _, v := range option.Options {
				values = append(values, v.Value)
			}
			sb.WriteString("\n" + e.reasoningUsage(values))
		}
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}
	target := strings.Join(args, " ")
	if category == "model" {
		var valid bool
		target, valid = parseModelSwitchArgs(args)
		if !valid {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelUsage))
			return
		}
	}
	if index, err := strconv.Atoi(target); err == nil && index >= 1 && index <= len(option.Options) {
		target = option.Options[index-1].Value
	}
	for _, v := range option.Options {
		if strings.EqualFold(target, v.Value) || strings.EqualFold(target, v.Name) {
			target = v.Value
			break
		}
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	if err := cfg.SetConfigOption(ctx, option.ID, target); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChangeFailed, err))
		return
	}
	actual := sessionConfigForCategory(cfg.ConfigOptions(), category)
	if actual == nil || actual.CurrentValue != target {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChangeFailed, fmt.Errorf("agent did not confirm the selected value")))
		return
	}
	e.persistSessionDefaults(agent, key, msg.SessionKey, cfg.ConfigOptions())
	// Apply live without closing the session or erasing its history. Workspace
	// defaults are replayed into other sessions on their next turn and restart.
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSessionConfigChanged, option.Name, target))
}

func (e *Engine) persistSessionDefaults(agent Agent, key, sessionKey string, options []SessionConfigOption) {
	if e.projectState == nil {
		return
	}
	workspace := workspaceModelOverrideKey(key, sessionKey, agent)
	if workspace == "" {
		return
	}
	model, effort := "", ""
	if option := sessionConfigForCategory(options, "model"); option != nil {
		model = option.CurrentValue
		e.projectState.SetWorkspaceModelOverride(workspace, model)
	}
	if option := sessionConfigForCategory(options, "thought_level"); option != nil {
		effort = option.CurrentValue
		e.projectState.SetWorkspaceReasoningOverride(workspace, effort)
	}
	e.projectState.Save()
	if defaults, ok := agent.(interface{ SetSessionConfigDefaults(string, string) }); ok {
		defaults.SetSessionConfigDefaults(model, effort)
	}
}

func (e *Engine) applyWorkspaceSessionConfig(live AgentSession, agent Agent, key, sessionKey string) error {
	if e.projectState == nil || !usesSessionControls(agent) {
		return nil
	}
	cfg, ok := live.(SessionConfigurer)
	if !ok {
		return nil
	}
	workspace := workspaceModelOverrideKey(key, sessionKey, agent)
	for _, setting := range []struct{ category, value string }{
		{"model", e.projectState.WorkspaceModelOverride(workspace)},
		{"thought_level", e.projectState.WorkspaceReasoningOverride(workspace)},
	} {
		if setting.value == "" {
			continue
		}
		option := sessionConfigForCategory(cfg.ConfigOptions(), setting.category)
		if option == nil {
			return fmt.Errorf("saved %s setting is unsupported", setting.category)
		}
		if option.CurrentValue == setting.value {
			continue
		}
		ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
		err := cfg.SetConfigOption(ctx, option.ID, setting.value)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) persistWorkspaceEffortOverride(key, sessionKey string, agent Agent, effort string) {
	if e.projectState == nil || effort == "" {
		return
	}
	workspace := workspaceModelOverrideKey(key, sessionKey, agent)
	if workspace == "" {
		return
	}
	e.projectState.SetWorkspaceReasoningOverride(workspace, effort)
	e.projectState.Save()
}

func (e *Engine) restoreWorkspaceEffort(agent Agent, workspace string) {
	if e.projectState == nil || workspace == "" {
		return
	}
	effort := e.projectState.WorkspaceReasoningOverride(workspace)
	if effort == "" {
		return
	}
	if switcher, ok := agent.(ReasoningEffortSwitcher); ok {
		for _, valid := range switcher.AvailableReasoningEfforts() {
			if effort == valid {
				switcher.SetReasoningEffort(effort)
				return
			}
		}
	}
}

// accountUsageCommand retains /usage's account-quota meaning for ACP too.
func (e *Engine) sessionUsageCommand(p Platform, msg *Message, agent Agent, sessions *SessionManager, key string) {
	live := e.sessionForControl(p, msg, agent, sessions, key)
	if live == nil {
		return
	}
	reporter, ok := live.(UsageReporter)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAccountUsageUnavailable))
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	report, err := reporter.GetUsage(ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgUsageFetchFailed, err))
		return
	}
	if report == nil || len(report.Buckets) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAccountUsageUnavailable))
		return
	}
	e.reply(p, msg.ReplyCtx, formatUsageReport(report, e.i18n.CurrentLang()))
}
