package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// fakeMidTurnAgent is a scripted ACP agent run as a helper process.
// CC_ACP_FAKE=native mimics agy: a second session/prompt settles the first
// one at once and is folded into the running turn after the current tool.
// CC_ACP_FAKE=interrupt mimics an agent without native support: it only
// honours session/cancel and reports whether the cancel came mid-tool.
func fakeMidTurnAgent(mode string) {
	enc := json.NewEncoder(os.Stdout)
	var mu sync.Mutex
	write := func(v any) { mu.Lock(); _ = enc.Encode(v); mu.Unlock() }
	update := func(u map[string]any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fake-1", "update": u}})
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	tool := func(id, status string) {
		kind := "tool_call_update"
		if status == "in_progress" {
			kind = "tool_call"
		}
		update(map[string]any{"sessionUpdate": kind, "toolCallId": id, "status": status, "title": "sleep", "kind": "execute"})
	}
	text := func(t string) {
		update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": t}})
	}
	var first json.RawMessage
	var toolRunning bool
	cancelled := make(chan struct{}, 1)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	prompts := 0
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Prompt []struct{ Text string } `json:"prompt"`
			} `json:"params"`
		}
		_ = json.Unmarshal(sc.Bytes(), &req)
		switch req.Method {
		case "initialize":
			reply(req.ID, map[string]any{"protocolVersion": 1})
		case "session/new":
			reply(req.ID, map[string]any{"sessionId": "fake-1"})
		case "session/cancel":
			mu.Lock()
			running := toolRunning
			mu.Unlock()
			if running {
				text("EARLY_CANCEL")
			}
			cancelled <- struct{}{}
		case "session/prompt":
			prompts++
			id := req.ID
			prompt := ""
			if len(req.Params.Prompt) > 0 {
				prompt = req.Params.Prompt[0].Text
			}
			switch {
			case prompts == 1:
				first = id
				mu.Lock()
				toolRunning = true
				mu.Unlock()
				tool("t1", "in_progress")
				go func() {
					if mode == "interrupt" {
						time.Sleep(400 * time.Millisecond)
						mu.Lock()
						toolRunning = false
						mu.Unlock()
						tool("t1", "completed")
						<-cancelled
						reply(id, map[string]any{"stopReason": "cancelled"})
					}
				}()
			case mode == "native":
				reply(first, map[string]any{"stopReason": "end_turn"})
				go func() {
					time.Sleep(300 * time.Millisecond)
					tool("t1", "completed")
					update(map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": prompt}})
					text("continued with " + prompt)
					reply(id, map[string]any{"stopReason": "end_turn"})
				}()
			default:
				go func() {
					text("resumed: " + strings.ReplaceAll(prompt, "\n", " "))
					reply(id, map[string]any{"stopReason": "end_turn"})
				}()
			}
		}
	}
}

func startFakeMidTurnSession(t *testing.T, mode, midTurn string) *acpSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := newACPSession(ctx, acpSessionConfig{
		command:      os.Args[0],
		args:         []string{"-test.run=TestFakeMidTurnAgentHelper"},
		extraEnv:     []string{"CC_ACP_FAKE=" + mode},
		workDir:      t.TempDir(),
		midTurnInput: midTurn,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFakeMidTurnAgentHelper(t *testing.T) {
	if mode := os.Getenv("CC_ACP_FAKE"); mode != "" {
		fakeMidTurnAgent(mode)
		os.Exit(0)
	}
}

// collectTurn reads events until the first EventResult, then checks that no
// second result follows shortly after.
func collectTurn(t *testing.T, s *acpSession) (text string, results int, errs []error) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for results == 0 {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventText:
				text += ev.Content
			case core.EventError:
				errs = append(errs, ev.Error)
			case core.EventResult:
				results++
			}
		case <-timeout:
			t.Fatalf("no result; text so far %q", text)
		}
	}
	extra := time.After(300 * time.Millisecond)
	for {
		select {
		case ev := <-s.Events():
			if ev.Type == core.EventResult {
				results++
			}
			if ev.Type == core.EventError {
				errs = append(errs, ev.Error)
			}
		case <-extra:
			return
		}
	}
}

func TestMidTurnPromptIsFoldedIntoRunningTurn(t *testing.T) {
	s := startFakeMidTurnSession(t, "native", midTurnPrompt)
	if !s.AcceptsMidTurnInput() {
		t.Fatal("prompt mode must accept mid-turn input")
	}
	go func() { _ = s.Send("original task", "", nil, nil) }()
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	if err := s.Send("add PINEAPPLE", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("mid-turn Send blocked for %s", d)
	}
	text, results, errs := collectTurn(t, s)
	if results != 1 || len(errs) != 0 {
		t.Fatalf("results=%d errs=%v; the early-settled first prompt must not end the turn", results, errs)
	}
	if !strings.Contains(text, "continued with add PINEAPPLE") {
		t.Fatalf("interjection lost: %q", text)
	}
}

func TestMidTurnInterruptWaitsForToolThenReprompts(t *testing.T) {
	s := startFakeMidTurnSession(t, "interrupt", midTurnInterrupt)
	go func() { _ = s.Send("original task", "", nil, nil) }()
	time.Sleep(100 * time.Millisecond)
	if err := s.Send("use the staging data", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	text, results, errs := collectTurn(t, s)
	if results != 1 || len(errs) != 0 {
		t.Fatalf("results=%d errs=%v; the cancelled prompt must not end the turn", results, errs)
	}
	if strings.Contains(text, "EARLY_CANCEL") {
		t.Fatalf("cancelled while the tool was still running: %q", text)
	}
	if !strings.Contains(text, "use the staging data") || !strings.Contains(text, "continue the original task") {
		t.Fatalf("re-prompt missing the interjection note: %q", text)
	}
	// The conversation is idle again: the next Send is an ordinary turn.
	go func() { _ = s.Send("next", "", nil, nil) }()
	if text, results, _ := collectTurn(t, s); results != 1 || !strings.Contains(text, "resumed: next") {
		t.Fatalf("next turn: results=%d text=%q", results, text)
	}
}

func TestMidTurnInputOption(t *testing.T) {
	for in, want := range map[string]string{"": midTurnQueue, "queue": midTurnQueue, "prompt": midTurnPrompt, "Interrupt": midTurnInterrupt} {
		got, err := normalizeMidTurnInput(in)
		if err != nil || got != want {
			t.Fatalf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := normalizeMidTurnInput("steer"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	a, err := New(map[string]any{"cmd": "echo", "mid_turn_input": "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).WorkspaceAgentOptions()["mid_turn_input"]; got != "prompt" {
		t.Fatalf("workspace agents lose mid_turn_input: %v", got)
	}
}
