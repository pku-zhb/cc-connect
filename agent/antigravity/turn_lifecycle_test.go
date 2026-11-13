package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func writeFakeTurnAgy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agy.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForResult(t *testing.T, s *antigravitySession, within time.Duration) (string, []error) {
	t.Helper()
	var text strings.Builder
	var errs []error
	timeout := time.After(within)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventError:
				errs = append(errs, ev.Error)
			case core.EventResult:
				return text.String(), errs
			}
		case <-timeout:
			t.Fatalf("no result within %s", within)
		}
	}
}

const fakeResult = `{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"done"}}`

// A second message to a busy conversation must never start a second agy
// process on the same --conversation.
func TestSendRefusesConcurrentTurnOnSameConversation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "starts")
	cmd := writeFakeTurnAgy(t, "echo start >> '"+marker+"'\nsleep 1\necho '"+fakeResult+"'\n")
	s, err := newAntigravitySession(context.Background(), cmd, []string{"--output-format", "stream-json"}, t.TempDir(), nil, "yolo", "conv-1", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("second", "", nil, nil); err == nil {
		t.Fatal("second Send during a running turn must be refused")
	}
	if text, errs := waitForResult(t, s, 5*time.Second); text != "done" || len(errs) != 0 {
		t.Fatalf("text=%q errs=%v", text, errs)
	}
	// The next turn is accepted once the result has been delivered.
	if err := s.Send("third", "", nil, nil); err != nil {
		t.Fatalf("Send after result: %v", err)
	}
	waitForResult(t, s, 5*time.Second)
	data, _ := os.ReadFile(marker)
	if n := strings.Count(string(data), "start"); n != 2 {
		t.Fatalf("agy started %d times, want 2", n)
	}
}

// A process still holding stdout after its result (lingering agy or a
// grandchild) must not keep the turn open: the result is delivered right away
// and the agy process is reaped.
func TestResultFinalizesTurnEvenIfStdoutStaysOpen(t *testing.T) {
	old := agyExitGrace
	agyExitGrace = 300 * time.Millisecond
	defer func() { agyExitGrace = old }()

	cmd := writeFakeTurnAgy(t, "echo '"+fakeResult+"'\nexec sleep 30\n")
	s, err := newAntigravitySession(context.Background(), cmd, []string{"--output-format", "stream-json"}, t.TempDir(), nil, "yolo", "conv-1", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now()
	if err := s.Send("hi", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	text, errs := waitForResult(t, s, 5*time.Second)
	if text != "done" || len(errs) != 0 {
		t.Fatalf("text=%q errs=%v", text, errs)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("result took %s; turn waited on the lingering process", elapsed)
	}
	if s.turnActive.Load() {
		t.Fatal("turn still marked active after result")
	}
}

func TestAntigravitySessionDoesNotAcceptMidTurnInput(t *testing.T) {
	var s core.AgentSession = &antigravitySession{}
	m, ok := s.(core.MidTurnInputSession)
	if !ok || m.AcceptsMidTurnInput() {
		t.Fatal("agy sessions must ask the engine to queue mid-turn input")
	}
}
