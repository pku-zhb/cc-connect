package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func quotaUpdate(session, kind string, usage any) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"sessionId": session, "update": map[string]any{
		"sessionUpdate": kind, "used": 4242, "size": 10000,
		"_meta": map[string]any{"museSubscriptionUsage": map[string]any{"source": "msp-host-observation", "billing": false, "usage": usage}},
	}})
	return raw
}

func quotaFixture(observed int64, used int) map[string]any {
	return map[string]any{"tier": "opaque-provider-tier", "observedAtMs": observed,
		"window": map[string]any{"usedPercent": used, "windowDurationMins": 300, "resetsAtMs": int64(2000000000000)},
		"weekly": map[string]any{"usedPercent": 42, "resetsAtMs": int64(2000500000000)},
	}
}

func TestAccountUsage_DecodesQuotaInsteadOfContext(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.onNotification("session/update", quotaUpdate("test-session-id", "usage_update", quotaFixture(1900000000123, 23)))
	report, err := s.GetUsage(context.Background())
	if err != nil || report == nil {
		t.Fatalf("quota missing: %+v %v", report, err)
	}
	if report.Provider != "Muse" || report.ObservedAtUnix != 1900000000 || len(report.Buckets) != 1 {
		t.Fatalf("bad report: %+v", report)
	}
	windows := report.Buckets[0].Windows
	expectedReset := int64(2000000000) - time.Now().Unix()
	if delta := int64(windows[0].ResetAfterSeconds) - expectedReset; delta < -2 || delta > 2 {
		t.Fatalf("reset countdown missing or stale: %+v", windows[0])
	}

	if len(windows) != 2 || windows[0].UsedPercent != 23 || windows[0].WindowSeconds != 18000 || windows[0].ResetAtUnix != 2000000000 || windows[1].UsedPercent != 42 || windows[1].WindowSeconds != 604800 {
		t.Fatalf("bad windows: %+v", windows)
	}
	if context := s.GetContextUsage(); context == nil || context.UsedTokens != 4242 {
		t.Fatal("context reporting was lost")
	}
	windows[0].UsedPercent = 99
	again, _ := s.GetUsage(context.Background())
	if again.Buckets[0].Windows[0].UsedPercent != 23 {
		t.Fatal("caller mutated the quota cache")
	}
}

func TestAccountUsage_MetadataOnlyZeroStaleAndClear(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.onNotification("session/update", quotaUpdate("test-session-id", "session_info_update", quotaFixture(1900000000000, 0)))
	report, _ := s.GetUsage(context.Background())
	if report == nil || report.Buckets[0].Windows[0].UsedPercent != 0 {
		t.Fatal("reported zero must mean full allowance")
	}
	s.onNotification("session/update", quotaUpdate("test-session-id", "usage_update", quotaFixture(1800000000000, 80)))
	s.onNotification("session/update", quotaUpdate("another-session", "usage_update", quotaFixture(2000000000000, 90)))
	report, _ = s.GetUsage(context.Background())
	if report.Buckets[0].Windows[0].UsedPercent != 0 {
		t.Fatal("stale or cross-session data replaced quota")
	}
	s.onNotification("session/update", quotaUpdate("test-session-id", "session_info_update", nil))
	report, _ = s.GetUsage(context.Background())
	if report != nil {
		t.Fatal("authoritative null must clear the account snapshot")
	}
	select {
	case event := <-s.Events():
		t.Fatalf("quota became conversation: %+v", event)
	default:
	}
}

func TestAccountUsage_ContextAndIncompleteMetadataAreNotQuota(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"usage_update","used":4242,"size":10000,"cost":{"amount":1,"currency":"USD"}}}`))
	bad := quotaFixture(1900000000000, 23)
	delete(bad, "weekly")
	s.onNotification("session/update", quotaUpdate("test-session-id", "usage_update", bad))
	report, err := s.GetUsage(context.Background())
	if err != nil || report != nil {
		t.Fatalf("context/cost or missing fields invented quota: %+v %v", report, err)
	}
}

func TestAccountUsage_WaitsForInitialPostLoadObservation(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.accountUsageReady = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		report, err := s.GetUsage(ctx)
		if err == nil && report == nil {
			err = context.DeadlineExceeded
		}
		done <- err
	}()
	s.onNotification("session/update", quotaUpdate("test-session-id", "session_info_update", quotaFixture(1900000000000, 23)))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
