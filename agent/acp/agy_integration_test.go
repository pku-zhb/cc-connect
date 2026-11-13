package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPSessionDoesNotAcceptMidTurnInput(t *testing.T) {
	var s core.AgentSession = &acpSession{}
	m, ok := s.(core.MidTurnInputSession)
	if !ok || m.AcceptsMidTurnInput() {
		t.Fatal("ACP sessions must ask the engine to queue mid-turn input")
	}
}

// Exercises the real Antigravity CLI "agy acp" end to end: thought chunks are
// streamed as EventThinking, the turn finishes, and the conversation resumes
// in a fresh process. Opt in with CC_AGY_ACP_LIVE=1 (uses the local agy login).
func TestAntigravityCLI_ACPThoughtsAndResume(t *testing.T) {
	if os.Getenv("CC_AGY_ACP_LIVE") != "1" {
		t.Skip("set CC_AGY_ACP_LIVE=1 to run against the local agy")
	}
	home, _ := os.UserHomeDir()
	agy := filepath.Join(home, ".local/bin/agy")
	if _, err := os.Stat(agy); err != nil {
		t.Skip("agy not installed:", err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("alpha line\nbeta line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := acpSessionConfig{
		command:     agy,
		args:        []string{"acp", "--dangerously-skip-permissions"},
		workDir:     work,
		initialMode: "accept-edits",
	}
	turn := func(s *acpSession, prompt string) (text string, thoughts, tools int) {
		done := make(chan error, 1)
		go func() { done <- s.Send(prompt, "", nil, nil) }()
		timeout := time.After(4 * time.Minute)
		for {
			select {
			case ev := <-s.Events():
				switch ev.Type {
				case core.EventThinking:
					thoughts++
				case core.EventToolUse:
					tools++
				case core.EventText:
					text += ev.Content
				case core.EventPermissionRequest:
					t.Fatalf("unexpected permission prompt: %+v", ev)
				case core.EventError:
					t.Fatalf("agent error: %v", ev.Error)
				case core.EventResult:
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					return
				}
			case <-timeout:
				t.Fatal("turn timed out")
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	s, err := newACPSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sid := s.CurrentSessionID()
	text, thoughts, tools := turn(s, "Run `wc -l notes.txt` in the shell, then read notes.txt, then reply with just its second line.")
	t.Logf("turn 1: session=%s thoughts=%d tools=%d text=%q", sid, thoughts, tools, text)
	_ = s.Close()
	if !strings.Contains(text, "beta line") || tools == 0 {
		t.Fatalf("unexpected first turn: text=%q tools=%d", text, tools)
	}
	if thoughts == 0 {
		t.Fatal("no thought chunks streamed")
	}

	cfg.resumeSessionID = sid
	s2, err := newACPSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if s2.CurrentSessionID() != sid {
		t.Fatalf("resumed session id %q, want %q", s2.CurrentSessionID(), sid)
	}
	text, _, _ = turn(s2, "Without using any tools, what was the first line of notes.txt? Reply with just that line.")
	t.Logf("turn 2: text=%q", text)
	if !strings.Contains(text, "alpha line") {
		t.Fatalf("resume lost context: %q", text)
	}
}
