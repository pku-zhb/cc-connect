package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (*Agent) UsesSessionControls() bool { return true }

type configValue struct {
	Value, Name, Description string
	Options                  []configValue `json:"options"`
}

type configOption struct {
	ID, Name, Description, Category, Type string
	CurrentValue                          json.RawMessage `json:"currentValue"`
	Options                               []configValue
}

func parseConfigOptions(raw json.RawMessage) ([]core.SessionConfigOption, error) {
	var input []configOption
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	var options []core.SessionConfigOption
	for _, item := range input {
		// No boolean capability is advertised. Unknown future option types
		// must not prevent using other selectors in the same response.
		if item.Type != "select" || item.ID == "" {
			continue
		}
		var current string
		if err := json.Unmarshal(item.CurrentValue, &current); err != nil {
			continue
		}
		option := core.SessionConfigOption{ID: item.ID, Name: item.Name, Description: item.Description, Category: item.Category, CurrentValue: current}
		for _, value := range item.Options {
			values := []configValue{value}
			if value.Options != nil {
				values = value.Options
			}
			for _, v := range values {
				option.Options = append(option.Options, core.SessionConfigValue{Value: v.Value, Name: v.Name, Description: v.Description})
			}
		}
		options = append(options, option)
	}
	return options, nil
}

func (s *acpSession) absorbConfigJSON(raw json.RawMessage) error {
	var response struct {
		Options json.RawMessage `json:"configOptions"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if len(response.Options) == 0 {
		return nil
	}
	options, err := parseConfigOptions(response.Options)
	if err != nil {
		return fmt.Errorf("acp: invalid configOptions: %w", err)
	}
	s.controlsMu.Lock()
	s.configOptions = options
	s.controlsMu.Unlock()
	// Approval policy and session behavior are separate ACP selectors. /mode
	// controls tool approval when that selector exists; Plan/Read-only must not
	// accidentally shadow the advertised approval policy.
	mode := s.configForCategory("approval_mode")
	if mode == nil {
		mode = s.configForCategory("mode")
	}
	if mode != nil {
		option := *mode
		block := &acpModesBlock{CurrentModeID: option.CurrentValue}
		for _, v := range option.Options {
			block.AvailableModes = append(block.AvailableModes, acpModeInfo{ID: v.Value, Name: v.Name, Description: v.Description})
		}
		s.absorbModes(block)
	}
	return nil
}

func (s *acpSession) ConfigOptions() []core.SessionConfigOption {
	s.controlsMu.RLock()
	defer s.controlsMu.RUnlock()
	options := append([]core.SessionConfigOption(nil), s.configOptions...)
	for i := range options {
		options[i].Options = append([]core.SessionConfigValue(nil), options[i].Options...)
	}
	return options
}

func (s *acpSession) SetConfigOption(ctx context.Context, id, value string) error {
	s.configWriteMu.Lock()
	defer s.configWriteMu.Unlock()
	valid := false
	for _, option := range s.ConfigOptions() {
		if option.ID != id {
			continue
		}
		for _, v := range option.Options {
			if v.Value == value {
				valid = true
				break
			}
		}
	}
	if !valid {
		return fmt.Errorf("acp: unsupported value %q for config option %q", value, id)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := s.tr.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.currentACPSessionID(), "configId": id, "value": value})
	if err != nil {
		return fmt.Errorf("acp: set config option: %w", err)
	}
	var response struct {
		Options json.RawMessage `json:"configOptions"`
	}
	if err := json.Unmarshal(result, &response); err != nil || len(response.Options) == 0 {
		return fmt.Errorf("acp: set_config_option response omitted configOptions")
	}
	if err := s.absorbConfigJSON(result); err != nil {
		return err
	}
	for _, option := range s.ConfigOptions() {
		if option.ID == id && option.CurrentValue == value {
			return nil
		}
	}
	return fmt.Errorf("acp: agent did not confirm config option %q as %q", id, value)
}

func (s *acpSession) AutoApprovesPermissions() bool {
	option := s.configForCategory("approval_mode")
	return option != nil && option.CurrentValue == "allowAll"
}

func (s *acpSession) configForCategory(category string) *core.SessionConfigOption {
	options := s.ConfigOptions()
	for i := range options {
		if options[i].Category == category {
			return &options[i]
		}
	}
	for i := range options {
		id := options[i].ID
		if id == category || (category == "thought_level" && (id == "reasoning_effort" || id == "reasoning")) {
			return &options[i]
		}
	}
	return nil
}

func (s *acpSession) GetModel() string {
	if option := s.configForCategory("model"); option != nil {
		return option.CurrentValue
	}
	return ""
}

func (s *acpSession) GetReasoningEffort() string {
	if option := s.configForCategory("thought_level"); option != nil {
		return option.CurrentValue
	}
	return ""
}

func (s *acpSession) applyInitialConfig(model, effort string) error {
	for _, setting := range []struct{ category, value string }{{"model", model}, {"thought_level", effort}} {
		if setting.value == "" {
			continue
		}
		option := s.configForCategory(setting.category)
		if option == nil {
			return fmt.Errorf("acp: saved %s setting is not supported", setting.category)
		}
		if option.CurrentValue == setting.value {
			continue
		}
		if err := s.SetConfigOption(s.ctx, option.ID, setting.value); err != nil {
			return err
		}
	}
	return nil
}

func (s *acpSession) absorbControlUpdate(raw json.RawMessage) bool {
	var wrap struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind       string          `json:"sessionUpdate"`
			Options    json.RawMessage `json:"configOptions"`
			Used, Size *int64
			Cost       *core.SessionCost
		} `json:"update"`
	}
	if json.Unmarshal(raw, &wrap) != nil {
		return false
	}
	if wrap.Update.Kind != "config_option_update" && wrap.Update.Kind != "usage_update" {
		return false
	}
	if id := s.currentACPSessionID(); id != "" && wrap.SessionID != "" && wrap.SessionID != id {
		return true
	}
	if wrap.Update.Kind == "config_option_update" {
		if len(wrap.Update.Options) > 0 {
			payload, _ := json.Marshal(map[string]json.RawMessage{"configOptions": wrap.Update.Options})
			if err := s.absorbConfigJSON(payload); err != nil {
				slog.Warn("acp: invalid configuration update", "error", err)
			}
		}
		return true
	}
	s.controlsMu.Lock()
	defer s.controlsMu.Unlock()
	if s.sessionUsage == nil {
		s.sessionUsage = &core.SessionUsage{}
	}
	if wrap.Update.Used != nil && *wrap.Update.Used >= 0 {
		s.sessionUsage.Used = wrap.Update.Used
	}
	if wrap.Update.Size != nil && *wrap.Update.Size > 0 {
		s.sessionUsage.Size = wrap.Update.Size
	}
	if cost := wrap.Update.Cost; cost != nil && cost.Amount >= 0 && !math.IsNaN(cost.Amount) && !math.IsInf(cost.Amount, 0) && strings.TrimSpace(cost.Currency) != "" {
		s.sessionUsage.Cost = cost
	}
	return true
}

func (s *acpSession) GetSessionUsage() *core.SessionUsage {
	s.controlsMu.RLock()
	defer s.controlsMu.RUnlock()
	if s.sessionUsage == nil {
		return nil
	}
	result := *s.sessionUsage
	if result.Used != nil {
		v := *result.Used
		result.Used = &v
	}
	if result.Size != nil {
		v := *result.Size
		result.Size = &v
	}
	if result.Cost != nil {
		v := *result.Cost
		result.Cost = &v
	}
	if result.Used == nil && result.Cost == nil {
		return nil
	}
	return &result
}

func (s *acpSession) GetContextUsage() *core.ContextUsage {
	u := s.GetSessionUsage()
	if u == nil || u.Used == nil || u.Size == nil {
		return nil
	}
	return &core.ContextUsage{UsedTokens: int(*u.Used), TotalTokens: int(*u.Used), ContextWindow: int(*u.Size)}
}

func (*acpSession) GetUsageSource() string { return "event" }

// Defaults belong to this workspace Agent instance, never to the shared
// project Agent. They apply to resumed and newly created ACP sessions.
func (a *Agent) SetSessionConfigDefaults(model, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	a.reasoningEffort = effort
}
