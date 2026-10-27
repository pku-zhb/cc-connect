package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ACP context usage is not a subscription quota. Muse's adapter publishes
// account observations in a namespaced extension; only decode that extension
// here, leaving core independent of providers and protocol-specific metadata.
func (s *acpSession) absorbAccountUsage(raw json.RawMessage) {
	var update struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &update) != nil {
		return
	}
	extension, present := update.Meta["museSubscriptionUsage"]
	if !present {
		return
	}
	var observation struct {
		Source string          `json:"source"`
		Usage  json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(extension, &observation) != nil || observation.Source != "msp-host-observation" || len(observation.Usage) == 0 {
		return
	}
	var report *core.UsageReport
	if string(observation.Usage) != "null" {
		var usage struct {
			ObservedAtMs *int64 `json:"observedAtMs"`
			Window       struct {
				UsedPercent        *int   `json:"usedPercent"`
				WindowDurationMins *int   `json:"windowDurationMins"`
				ResetsAtMs         *int64 `json:"resetsAtMs"`
			} `json:"window"`
			Weekly struct {
				UsedPercent *int   `json:"usedPercent"`
				ResetsAtMs  *int64 `json:"resetsAtMs"`
			} `json:"weekly"`
		}
		if json.Unmarshal(observation.Usage, &usage) != nil || usage.ObservedAtMs == nil || *usage.ObservedAtMs <= 0 ||
			usage.Window.UsedPercent == nil || *usage.Window.UsedPercent < 0 || usage.Window.WindowDurationMins == nil || *usage.Window.WindowDurationMins <= 0 || *usage.Window.WindowDurationMins > 525600 || usage.Window.ResetsAtMs == nil || *usage.Window.ResetsAtMs <= 0 ||
			usage.Weekly.UsedPercent == nil || *usage.Weekly.UsedPercent < 0 || usage.Weekly.ResetsAtMs == nil || *usage.Weekly.ResetsAtMs <= 0 {
			return
		}
		window, weekly := *usage.Window.UsedPercent, *usage.Weekly.UsedPercent
		report = &core.UsageReport{Provider: "Muse", ObservedAtUnix: *usage.ObservedAtMs / 1000,
			Buckets: []core.UsageBucket{{Name: "Subscription", Allowed: window < 100 && weekly < 100, LimitReached: window >= 100 || weekly >= 100,
				Windows: []core.UsageWindow{
					{Name: "Primary", UsedPercent: window, WindowSeconds: *usage.Window.WindowDurationMins * 60, ResetAtUnix: *usage.Window.ResetsAtMs / 1000},
					{Name: "Weekly", UsedPercent: weekly, WindowSeconds: 7 * 24 * 60 * 60, ResetAtUnix: *usage.Weekly.ResetsAtMs / 1000},
				},
			}},
		}
	}
	s.controlsMu.Lock()
	if report == nil || s.accountUsage == nil || report.ObservedAtUnix >= s.accountUsage.ObservedAtUnix {
		s.accountUsage = report
	}
	s.controlsMu.Unlock()
	if s.accountUsageReady != nil {
		s.accountUsageOnce.Do(func() { close(s.accountUsageReady) })
	}
}

func (s *acpSession) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	// The adapter can emit its initial quota immediately after session/load's
	// response. Give that first observation a bounded opportunity to arrive.
	if s.accountUsageReady != nil {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-s.accountUsageReady:
		case <-ctx.Done():
			return nil, fmt.Errorf("acp: account usage: %w", ctx.Err())
		case <-timer.C:
		}
	}
	s.controlsMu.RLock()
	defer s.controlsMu.RUnlock()
	if s.accountUsage == nil {
		return nil, nil
	}
	result := *s.accountUsage
	result.Buckets = append([]core.UsageBucket(nil), result.Buckets...)
	for i := range result.Buckets {
		result.Buckets[i].Windows = append([]core.UsageWindow(nil), result.Buckets[i].Windows...)
		for j := range result.Buckets[i].Windows {
			window := &result.Buckets[i].Windows[j]
			if window.ResetAtUnix > 0 {
				remaining := window.ResetAtUnix - time.Now().Unix()
				if remaining < 0 {
					remaining = 0
				}
				window.ResetAfterSeconds = int(remaining)
			}
		}
	}
	return &result, nil
}
