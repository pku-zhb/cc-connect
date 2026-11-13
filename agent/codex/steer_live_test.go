package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Live check that /ps text reaches a running Codex app-server turn through
// turn/steer. Opt in with CC_CODEX_LIVE_CMD=<codex launcher or binary>.
func TestLiveAppServerSteerReachesRunningTurn(t *testing.T) {
	cmd := os.Getenv("CC_CODEX_LIVE_CMD")
	if cmd == "" {
		t.Skip("set CC_CODEX_LIVE_CMD to run against a local codex")
	}
	model := os.Getenv("CC_CODEX_LIVE_MODEL")
	a, err := New(map[string]any{
		"cmd": cmd, "backend": "app_server", "mode": "yolo",
		"model": model, "reasoning_effort": "low", "work_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	steerer, ok := s.(core.UserInputSteerer)
	if !ok {
		t.Fatal("app-server session does not support steering")
	}
	if err := s.Send("Run these three shell commands one at a time, as three separate tool calls: `sleep 6; echo one`, then `sleep 6; echo two`, then `sleep 6; echo three`. After all three, reply with a one-line summary.", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	steered := false
	var text string
	tools := 0
	timeout := time.After(5 * time.Minute)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventToolUse:
				tools++
				if !steered {
					ok, err := steerer.SteerUserInput("Interjection: in your final summary, also include the word PINEAPPLE.")
					if err != nil || !ok {
						t.Fatalf("steer ok=%v err=%v", ok, err)
					}
					steered = true
				}
			case core.EventText:
				text += ev.Content
			case core.EventError:
				t.Fatalf("agent error: %v", ev.Error)
			case core.EventResult:
				if ev.Content != "" {
					text += ev.Content
				}
				t.Logf("tools=%d text=%q", tools, text)
				if !steered || !strings.Contains(strings.ToUpper(text), "PINEAPPLE") {
					t.Fatalf("steered input did not reach the turn: %q", text)
				}
				return
			}
		case <-timeout:
			t.Fatalf("timed out; text=%q", text)
		}
	}
}
