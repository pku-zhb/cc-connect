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

// liveMidTurn runs a three-step shell task, sends /ps-style input after the
// first tool call starts, and checks the input reached the same turn.
func liveMidTurn(t *testing.T, cfg acpSessionConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	s, err := newACPSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	go func() {
		_ = s.Send("Run these three shell commands one at a time, as three separate tool calls: `sleep 6; echo one`, then `sleep 6; echo two`, then `sleep 6; echo three`. After all three, reply with a one-line summary.", "", nil, nil)
	}()
	injected := false
	var text string
	results, tools := 0, 0
	timeout := time.After(5 * time.Minute)
	for results == 0 {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventToolUse:
				tools++
				if !injected {
					injected = true
					start := time.Now()
					if err := s.Send("Interjection: in your final summary, also include the word PINEAPPLE.", "", nil, nil); err != nil {
						t.Fatal(err)
					}
					t.Logf("mid-turn Send returned in %s", time.Since(start))
				}
			case core.EventText:
				text += ev.Content
			case core.EventError:
				t.Fatalf("agent error: %v", ev.Error)
			case core.EventPermissionRequest:
				t.Fatalf("unexpected permission prompt: %+v", ev)
			case core.EventResult:
				results++
			}
		case <-timeout:
			t.Fatalf("turn timed out; text=%q", text)
		}
	}
	extra := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-s.Events():
			if ev.Type == core.EventResult || ev.Type == core.EventError {
				t.Fatalf("second terminal event after the turn: %+v", ev)
			}
		case <-extra:
			done = true
		}
	}
	t.Logf("tools=%d text=%q", tools, text)
	if !strings.Contains(strings.ToUpper(text), "PINEAPPLE") {
		t.Fatalf("interjection did not reach the turn: %q", text)
	}
}

func TestAntigravityCLI_ACPMidTurnPrompt(t *testing.T) {
	if os.Getenv("CC_AGY_ACP_LIVE") != "1" {
		t.Skip("set CC_AGY_ACP_LIVE=1 to run against the local agy")
	}
	home, _ := os.UserHomeDir()
	liveMidTurn(t, acpSessionConfig{
		command:      filepath.Join(home, ".local/bin/agy"),
		args:         []string{"acp", "--dangerously-skip-permissions"},
		workDir:      t.TempDir(),
		initialMode:  "accept-edits",
		midTurnInput: midTurnPrompt,
	})
}

// CC_MUSE_ACP_CMD names the Muse ACP launcher; Muse lacks native mid-turn
// input, so this exercises the interrupt path.
func TestMuseACPMidTurnInterrupt(t *testing.T) {
	cmd := os.Getenv("CC_MUSE_ACP_CMD")
	if cmd == "" {
		t.Skip("set CC_MUSE_ACP_CMD to run against the local Muse ACP launcher")
	}
	liveMidTurn(t, acpSessionConfig{
		command:      cmd,
		workDir:      t.TempDir(),
		initialMode:  "allowAll",
		midTurnInput: midTurnInterrupt,
	})
}
