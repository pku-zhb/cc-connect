package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveUsageBackend_ExplicitWins(t *testing.T) {
	got, err := resolveUsageBackend("muse", "/opt/agy-slack-cli.py", "Antigravity")
	if err != nil || got != usageBackendMuse {
		t.Fatalf("explicit muse should win over agy identity: got %q err %v", got, err)
	}
	got, err = resolveUsageBackend("agy", "/opt/muse-slack-cli.py", "Muse Code")
	if err != nil || got != usageBackendAgy {
		t.Fatalf("explicit agy should win over muse identity: got %q err %v", got, err)
	}
}

func TestResolveUsageBackend_Invalid(t *testing.T) {
	_, err := resolveUsageBackend("codex", "agy", "Antigravity")
	if err == nil || !strings.Contains(err.Error(), "unsupported usage_backend") {
		t.Fatalf("expected invalid backend error, got %v", err)
	}
}

func TestInferUsageBackend_CommandAndDisplayName(t *testing.T) {
	cases := []struct {
		command, name string
		want          usageBackend
	}{
		{"/Users/zhuhuibin/services/cc-connect/launchers/agy-slack-cli.py", "Antigravity", usageBackendAgy},
		{"agy", "", usageBackendAgy},
		{"/usr/local/bin/antigravity", "", usageBackendAgy},
		{"/Users/zhuhuibin/services/cc-connect/launchers/muse-slack-cli.py", "Muse Code", usageBackendMuse},
		{"muse", "", usageBackendMuse},
		{"true", "ACP", usageBackendNone},
		{"agent", "Cursor ACP", usageBackendNone},
		{"agy-and-muse-cli.py", "ACP", usageBackendNone},
		{"true", "Muse Antigravity", usageBackendNone},
	}
	for _, tc := range cases {
		got := inferUsageBackend(tc.command, tc.name)
		if got != tc.want {
			t.Errorf("infer(%q, %q) = %q, want %q", tc.command, tc.name, got, tc.want)
		}
	}
}

func TestNew_ResolvesUsageBackendFromOptions(t *testing.T) {
	dir := t.TempDir()
	agy := filepath.Join(dir, "agy-slack-cli.py")
	if err := os.WriteFile(agy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent, err := New(map[string]any{"command": agy, "display_name": "Antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	a := agent.(*Agent)
	if a.usageBackend != usageBackendAgy {
		t.Fatalf("inferred backend = %q, want agy", a.usageBackend)
	}
	if got := a.WorkspaceAgentOptions()["usage_backend"]; got != "agy" {
		t.Fatalf("workspace options should persist resolved backend, got %v", got)
	}

	explicit, err := New(map[string]any{"command": agy, "display_name": "Antigravity", "usage_backend": "muse"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.(*Agent).usageBackend != usageBackendMuse {
		t.Fatalf("explicit option was ignored: %q", explicit.(*Agent).usageBackend)
	}
}

func TestGetUsage_DispatchesByBackend(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		s, _, _ := newTestSession(t, nil)
		s.usageBackend = usageBackendNone
		_, err := s.GetUsage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "usage backend is not configured") {
			t.Fatalf("unknown backend: err=%v", err)
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		s, _, _ := newTestSession(t, nil)
		s.usageBackend = usageBackend("cursor")
		_, err := s.GetUsage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "unsupported usage backend") {
			t.Fatalf("unsupported backend: err=%v", err)
		}
	})
	t.Run("muse does not call agy CLI", func(t *testing.T) {
		s, _, _ := newTestSession(t, nil)
		s.usageBackend = usageBackendMuse
		s.cliRunner = func(context.Context, string, []string, string, []string) ([]byte, error) {
			t.Fatal("muse branch called the CLI runner")
			return nil, errors.New("unreachable")
		}
		report, err := s.GetUsage(context.Background())
		if err != nil || report != nil {
			t.Fatalf("muse without observation should return nil report, got %+v %v", report, err)
		}
	})
	t.Run("agy does not wait for muse observations", func(t *testing.T) {
		s, _, _ := newTestSession(t, nil)
		s.usageBackend = usageBackendAgy
		s.command = "/opt/agy-slack-cli.py"
		s.displayName = "Antigravity"
		s.accountUsageReady = make(chan struct{}) // would block the muse branch for 1s
		s.cliRunner = func(_ context.Context, name string, args []string, _ string, _ []string) ([]byte, error) {
			if name != "/opt/agy-slack-cli.py" {
				t.Fatalf("command = %q", name)
			}
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "/usage"):
				return []byte(agyUsageFixtureJSON), nil
			case strings.Contains(joined, "/credits"):
				return []byte(agyCreditsFixtureJSON), nil
			default:
				return nil, errors.New("unexpected args: " + joined)
			}
		}
		start := time.Now()
		report, err := s.GetUsage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) >= time.Second {
			t.Fatal("agy branch waited on the muse observation channel")
		}
		if report == nil || report.Provider != "Antigravity" || len(report.Buckets) != 2 {
			t.Fatalf("bad agy report: %+v", report)
		}
		if report.Buckets[0].Name != "Gemini Models" || report.Buckets[1].Name != "Claude and GPT models" {
			t.Fatalf("group names: %+v", report.Buckets)
		}
		if report.Credits == nil || report.Credits.Balance != "0" {
			t.Fatalf("credits missing: %+v", report.Credits)
		}
		// A later muse observation must not be used as a fallback, even
		// after the agy cache is cleared so GetUsage hits the branch again.
		s.controlsMu.Lock()
		s.agyUsage, s.agyUsageAt = nil, time.Time{}
		s.controlsMu.Unlock()
		s.onNotification("session/update", quotaUpdate("test-session-id", "usage_update", quotaFixture(1900000000123, 23)))
		again, err := s.GetUsage(context.Background())
		if err != nil || again.Provider != "Antigravity" || len(again.Buckets) != 2 {
			t.Fatalf("agy result replaced by muse observation: %+v %v", again, err)
		}
	})
	t.Run("agy reports its own CLI error", func(t *testing.T) {
		s, _, _ := newTestSession(t, nil)
		s.usageBackend = usageBackendAgy
		s.command = "/opt/agy-slack-cli.py"
		s.cliRunner = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "/usage") {
				return nil, errors.New("agy: quota endpoint failed")
			}
			return []byte(agyCreditsFixtureJSON), nil
		}
		_, err := s.GetUsage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "agy /usage") || !strings.Contains(err.Error(), "quota endpoint failed") {
			t.Fatalf("expected agy /usage error, got %v", err)
		}
	})
}
