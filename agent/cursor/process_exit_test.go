package cursor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// writeFakeAgent creates an executable shell script standing in for the
// Cursor Agent CLI. It receives the usual --print/--workspace/-- args and
// simply runs the given shell body.
func writeFakeAgent(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	return path
}

// drainUntilTerminal reads events until a terminal one (EventError or a
// Done EventResult) arrives, or fails after timeout. It returns the
// terminal event.
func drainUntilTerminal(t *testing.T, cs *cursorSession, timeout time.Duration) core.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case evt := <-cs.Events():
			if evt.Type == core.EventError || (evt.Type == core.EventResult && evt.Done) {
				return evt
			}
		case <-deadline:
			t.Fatal("no terminal event after agent process exited (turn would hang)")
			return core.Event{}
		}
	}
}

// TestProcessExitWithoutResult_EmitsError reproduces the 2026-11-03 hang:
// the Cursor CLI process died mid-turn (TLS abort) with nothing on stderr
// and no result event. The session must surface a terminal error so the
// engine ends the turn instead of waiting for the idle timeout.
func TestProcessExitWithoutResult_EmitsError(t *testing.T) {
	script := writeFakeAgent(t, "echo '{\"type\":\"system\",\"session_id\":\"chat-exit-1\"}'\nexit 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, t.TempDir(), "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventError {
		t.Fatalf("terminal event type = %q, want EventError", evt.Type)
	}
	if evt.Error == nil {
		t.Fatal("EventError must carry an error")
	}
	// The resumable chat ID learned from the system event must survive so
	// the next turn can --resume the same conversation.
	if got := cs.CurrentSessionID(); got != "chat-exit-1" {
		t.Fatalf("CurrentSessionID = %q, want chat-exit-1", got)
	}
}

// TestProcessDeathBeforeWork_RetriesOnce checks the bounded relaunch: a
// process that dies before emitting any assistant/tool activity (the
// startup connection-failure shape) is relaunched once with the same
// prompt, and the retried process's result completes the turn.
func TestProcessDeathBeforeWork_RetriesOnce(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	body := "c=0; [ -f '" + counter + "' ] && c=$(cat '" + counter + "'); c=$((c+1)); echo $c > '" + counter + "'\n" +
		"if [ $c -lt 2 ]; then exit 1; fi\n" +
		"echo '{\"type\":\"result\",\"result\":\"recovered\",\"session_id\":\"chat-retry-1\"}'\n"
	script := writeFakeAgent(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, dir, "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventResult || evt.Content != "recovered" {
		t.Fatalf("terminal event = %+v, want recovered result", evt)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if string(data) != "2\n" {
		t.Fatalf("agent invoked %q times, want exactly 2 (one relaunch)", string(data))
	}
}

// TestProcessDeathAfterWork_DoesNotRetry checks that a process which had
// already produced assistant output is NOT relaunched: its tool work may
// have had side effects, so the turn must end with an error instead.
func TestProcessDeathAfterWork_DoesNotRetry(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	body := "c=0; [ -f '" + counter + "' ] && c=$(cat '" + counter + "'); c=$((c+1)); echo $c > '" + counter + "'\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}'\n" +
		"exit 1\n"
	script := writeFakeAgent(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, dir, "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventError {
		t.Fatalf("terminal event type = %q, want EventError", evt.Type)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if string(data) != "1\n" {
		t.Fatalf("agent invoked %q times, want exactly 1 (no relaunch after work)", string(data))
	}
}

// A clean exit (code 0, no stderr) after the model already produced its
// closing assistant segment means the turn finished but the CLI never
// emitted its result event (seen 2026-11-03 13:33: the summary existed
// in the transcript yet the user got "exited without a result event").
// The last segment is the answer; deliver it instead of erroring.
func TestProcessCleanExitAfterFinalText_DeliversLastSegment(t *testing.T) {
	body := "echo '{\"type\":\"system\",\"session_id\":\"chat-clean-1\"}'\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"All done summary\"}]}}'\n" +
		"exit 0\n"
	script := writeFakeAgent(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, t.TempDir(), "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventResult || evt.Content != "All done summary" {
		t.Fatalf("terminal event = %+v, want synthesized result from last segment", evt)
	}
}

// If tool work happened after the last narration segment, that segment
// was a preface ("I will check X"), not a conclusion. A clean exit then
// means the turn died mid-work: never dress the preface up as the final
// answer; surface the failure instead (seen 2026-11-03 14:23, when a
// book-lookup turn died during web searches and its preface was
// delivered as if it were the completed reply).
func TestProcessCleanExitAfterPrefaceAndTools_EmitsError(t *testing.T) {
	body := "echo '{\"type\":\"system\",\"session_id\":\"chat-clean-2\"}'\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"I will check that\"}]}}'\n" +
		"echo '{\"type\":\"tool_call\",\"subtype\":\"started\",\"tool_call\":{\"shellToolCall\":{\"args\":{\"command\":\"ls\"}}}}'\n" +
		"exit 0\n"
	script := writeFakeAgent(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, t.TempDir(), "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventError {
		t.Fatalf("terminal event = %+v, want EventError (preface must not become the answer)", evt)
	}
}

// When the session context is cancelled (user /stop, service restart)
// the killed process must not surface as an agent error.
func TestProcessKilledByContextCancel_EmitsNoError(t *testing.T) {
	script := writeFakeAgent(t, "exec sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	cs, err := newCursorSession(ctx, script, nil, t.TempDir(), "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case evt := <-cs.Events():
		if evt.Type == core.EventError {
			t.Fatalf("cancelled turn surfaced error: %v", evt.Error)
		}
	case <-time.After(2 * time.Second):
	}
	cs.Close()
}

// TestProcessCleanExitWithoutResult_EmitsError covers a clean exit that
// produced no result event at all: still a dead turn, still must end.
func TestProcessCleanExitWithoutResult_EmitsError(t *testing.T) {
	script := writeFakeAgent(t, "exit 0\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := newCursorSession(ctx, script, nil, t.TempDir(), "", "force", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := drainUntilTerminal(t, cs, 5*time.Second)
	if evt.Type != core.EventError {
		t.Fatalf("terminal event type = %q, want EventError", evt.Type)
	}
}
