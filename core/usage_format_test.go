package core

import (
	"strings"
	"testing"
)

func TestFormatUsageReport_MultipleBucketsShowsAllGroups(t *testing.T) {
	report := &UsageReport{
		Provider:       "Antigravity",
		ObservedAtUnix: 0,
		Buckets: []UsageBucket{
			{
				Name:    "Gemini Models",
				Allowed: true,
				Windows: []UsageWindow{
					{Name: "Weekly Limit Remaining", UsedPercent: 12, WindowSeconds: 604800, ResetAfterSeconds: 5*24*3600 + 23*3600},
					{Name: "Five Hour Limit Remaining", UsedPercent: 0, WindowSeconds: 18000, ResetAfterSeconds: 2 * 3600},
				},
			},
			{
				Name:    "Claude and GPT models",
				Allowed: true,
				Windows: []UsageWindow{
					{Name: "Weekly Limit Remaining", UsedPercent: 0, WindowSeconds: 604800, ResetAfterSeconds: 6 * 24 * 3600},
					{Name: "Five Hour Limit Remaining", UsedPercent: 0, WindowSeconds: 18000, ResetAfterSeconds: 3 * 3600},
				},
			},
		},
		Credits: &UsageCredits{HasCredits: false, Balance: "0"},
	}

	got := formatUsageReport(report, LangChinese)
	for _, want := range []string{
		"Antigravity",
		"Gemini Models",
		"5小时限额",
		"剩余：88%",
		"7日限额",
		"剩余：100%",
		"Claude and GPT models",
		"Credits：0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report = %q, want substring %q", got, want)
		}
	}
	if c := strings.Count(got, "5小时限额"); c != 2 {
		t.Fatalf("5h windows rendered %d times, want 2: %q", c, got)
	}
	if c := strings.Count(got, "7日限额"); c != 2 {
		t.Fatalf("7d windows rendered %d times, want 2: %q", c, got)
	}
	// 5h should still be listed before 7d inside a group, even when the
	// source JSON puts weekly first.
	gemini := got[strings.Index(got, "Gemini Models"):strings.Index(got, "Claude and GPT models")]
	if strings.Index(gemini, "5小时限额") > strings.Index(gemini, "7日限额") {
		t.Fatalf("5h should be listed before 7d: %q", gemini)
	}
}

func TestFormatUsageReport_SingleBucketUnchangedAndOmitsBucketName(t *testing.T) {
	report := &UsageReport{
		Email: "dev@example.com",
		Plan:  "team",
		Buckets: []UsageBucket{{
			Name: "Rate limit",
			Windows: []UsageWindow{
				{Name: "Primary", UsedPercent: 23, WindowSeconds: 18000, ResetAfterSeconds: 6665},
				{Name: "Secondary", UsedPercent: 42, WindowSeconds: 604800, ResetAfterSeconds: 512698},
			},
		}},
	}
	got := formatUsageReport(report, LangEnglish)
	if strings.Contains(got, "Rate limit") {
		t.Fatalf("single-bucket report should omit the bucket heading: %q", got)
	}
	if strings.Contains(got, "Credits") {
		t.Fatalf("report without credits should omit the credits line: %q", got)
	}
	for _, want := range []string{
		"Account: dev@example.com (team)",
		"5h limit",
		"Remaining: 77%",
		"7d limit",
		"Remaining: 58%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report = %q, want substring %q", got, want)
		}
	}
}

func TestFormatUsageReport_EnglishMultipleBucketsAndCredits(t *testing.T) {
	report := &UsageReport{
		Provider: "Antigravity",
		Buckets: []UsageBucket{
			{Name: "Gemini Models", Windows: []UsageWindow{{UsedPercent: 12, WindowSeconds: 18000, ResetAfterSeconds: 3600}}},
			{Name: "Claude and GPT models", Windows: []UsageWindow{{UsedPercent: 0, WindowSeconds: 18000, ResetAfterSeconds: 3600}}},
		},
		Credits: &UsageCredits{HasCredits: true, Balance: "12.5"},
	}
	got := formatUsageReport(report, LangEnglish)
	for _, want := range []string{"Gemini Models", "Claude and GPT models", "Credits: 12.5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report = %q, want substring %q", got, want)
		}
	}
}
