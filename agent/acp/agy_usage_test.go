package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const agyUsageFixtureJSON = `{
  "conversation_id": "",
  "status": "SUCCESS",
  "command": {
    "name": "usage",
    "data": {
      "description": "Within each group, models share a weekly limit and a 5-hour limit.",
      "groups": [
        {
          "name": "Gemini Models",
          "description": "Models within this group: Gemini Flash, Gemini Pro",
          "buckets": [
            {
              "id": "gemini-weekly",
              "name": "Weekly Limit Remaining",
              "window": "weekly",
              "remaining_fraction": 0.8835835456848145,
              "reset_time": "2026-11-19T07:36:00Z"
            },
            {
              "id": "gemini-5h",
              "name": "Five Hour Limit Remaining",
              "window": "5h",
              "remaining_fraction": 1,
              "reset_time": "2026-11-13T12:40:03Z"
            }
          ]
        },
        {
          "name": "Claude and GPT models",
          "description": "Models within this group: Claude Opus, Claude Sonnet, GPT-OSS",
          "buckets": [
            {
              "id": "3p-weekly",
              "name": "Weekly Limit Remaining",
              "window": "weekly",
              "remaining_fraction": 1,
              "reset_time": "2026-11-20T07:53:59Z"
            },
            {
              "id": "3p-5h",
              "name": "Five Hour Limit Remaining",
              "window": "5h",
              "remaining_fraction": 1,
              "reset_time": "2026-11-13T12:53:59Z"
            }
          ]
        }
      ]
    }
  }
}`

const agyCreditsFixtureJSON = `{
  "status": "SUCCESS",
  "command": {
    "name": "credits",
    "data": {
      "remaining_credits": 0,
      "upgrade_uri": "https://antigravity.google/g1-upgrade"
    }
  }
}`

func TestParseAgyUsageJSON_TwoGroups(t *testing.T) {
	observed := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	report, err := parseAgyUsageJSON([]byte(agyUsageFixtureJSON), observed, "Antigravity")
	if err != nil {
		t.Fatal(err)
	}
	if report.Provider != "Antigravity" || report.ObservedAtUnix != observed.Unix() {
		t.Fatalf("header: %+v", report)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(report.Buckets))
	}

	gemini := report.Buckets[0]
	if gemini.Name != "Gemini Models" || !gemini.Allowed || gemini.LimitReached || len(gemini.Windows) != 2 {
		t.Fatalf("gemini bucket: %+v", gemini)
	}
	weekly, fiveH := gemini.Windows[0], gemini.Windows[1]
	if weekly.WindowSeconds != 604800 || weekly.UsedPercent != 12 || weekly.ResetAtUnix != time.Date(2026, 11, 19, 7, 36, 0, 0, time.UTC).Unix() {
		t.Fatalf("gemini weekly: %+v", weekly)
	}
	if fiveH.WindowSeconds != 18000 || fiveH.UsedPercent != 0 || fiveH.ResetAtUnix != time.Date(2026, 11, 13, 12, 40, 3, 0, time.UTC).Unix() {
		t.Fatalf("gemini 5h: %+v", fiveH)
	}

	third := report.Buckets[1]
	if third.Name != "Claude and GPT models" || len(third.Windows) != 2 {
		t.Fatalf("claude/gpt bucket: %+v", third)
	}
	if third.Windows[0].UsedPercent != 0 || third.Windows[1].UsedPercent != 0 {
		t.Fatalf("claude/gpt should be unused: %+v", third.Windows)
	}

	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	withCounts := usageReportWithCountdowns(report, now)
	wantWeeklyReset := int(time.Date(2026, 11, 19, 7, 36, 0, 0, time.UTC).Sub(now).Seconds())
	if withCounts.Buckets[0].Windows[0].ResetAfterSeconds != wantWeeklyReset {
		t.Fatalf("weekly countdown = %d, want %d", withCounts.Buckets[0].Windows[0].ResetAfterSeconds, wantWeeklyReset)
	}
	weekly.UsedPercent = 99
	if report.Buckets[0].Windows[0].UsedPercent != 12 {
		t.Fatal("caller should not need to worry about fixture mutation here")
	}
	_ = weekly
}

func TestUsedPercentFromRemaining(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{1, 0},
		{0.8835835456848145, 12}, // 88% remaining, as shown by agy
		{0, 100},
		{-0.1, 100},
		{1.2, 0},
	}
	for _, tc := range cases {
		if got := usedPercentFromRemaining(tc.in); got != tc.want {
			t.Errorf("usedPercentFromRemaining(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAgyCreditsJSON(t *testing.T) {
	credits, err := parseAgyCreditsJSON([]byte(agyCreditsFixtureJSON))
	if err != nil {
		t.Fatal(err)
	}
	if credits.HasCredits || credits.Balance != "0" {
		t.Fatalf("credits: %+v", credits)
	}
	nonzero, err := parseAgyCreditsJSON([]byte(`{"status":"SUCCESS","command":{"name":"credits","data":{"remaining_credits":12.5}}}`))
	if err != nil || !nonzero.HasCredits || nonzero.Balance != "12.5" {
		t.Fatalf("nonzero credits: %+v %v", nonzero, err)
	}
}

func TestParseAgyUsageJSON_Errors(t *testing.T) {
	now := time.Unix(0, 0)
	cases := []struct {
		name, raw, want string
	}{
		{"not json", `{`, "decode"},
		{"failed status", `{"status":"ERROR","command":{"name":"usage","data":{"groups":[]}}}`, "status"},
		{"no groups", `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`, "no quota groups"},
		{"missing fraction", `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","window":"weekly"}]}]}}}`, "remaining_fraction"},
	}
	for _, tc := range cases {
		_, err := parseAgyUsageJSON([]byte(tc.raw), now, "Antigravity")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want substring %q", tc.name, err, tc.want)
		}
	}
}

func TestAgyUsage_LiveCLI(t *testing.T) {
	command := strings.TrimSpace(os.Getenv("CC_AGY_LIVE_USAGE"))
	if command == "" {
		t.Skip("set CC_AGY_LIVE_USAGE to the agy binary or launcher to run this test")
	}
	s, _, _ := newTestSession(t, nil)
	s.usageBackend = usageBackendAgy
	s.command = command
	s.displayName = "Antigravity"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report, err := s.GetUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || len(report.Buckets) < 2 {
		t.Fatalf("expected both model groups, got %+v", report)
	}
	names := make([]string, 0, len(report.Buckets))
	for _, bucket := range report.Buckets {
		names = append(names, bucket.Name)
		if len(bucket.Windows) == 0 {
			t.Fatalf("group %q has no windows", bucket.Name)
		}
	}
	joined := strings.Join(names, "\n")
	if !strings.Contains(strings.ToLower(joined), "gemini") {
		t.Fatalf("missing Gemini group: %v", names)
	}
	if !strings.Contains(strings.ToLower(joined), "claude") && !strings.Contains(strings.ToLower(joined), "gpt") {
		t.Fatalf("missing Claude/GPT group: %v", names)
	}
	for _, bucket := range report.Buckets {
		for _, window := range bucket.Windows {
			t.Logf("live usage: group=%q window=%s used=%d reset_after=%ds", bucket.Name, window.Name, window.UsedPercent, window.ResetAfterSeconds)
		}
	}
	if report.Credits != nil {
		t.Logf("live usage: credits=%q has=%v", report.Credits.Balance, report.Credits.HasCredits)
	}
	t.Logf("live usage: provider=%q observed=%d", report.Provider, report.ObservedAtUnix)
}
