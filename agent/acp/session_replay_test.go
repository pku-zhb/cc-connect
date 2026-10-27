package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The server sends much more history than the event queue can hold, before
// acknowledging resume. No engine event reader exists during this handshake.
func TestHandshake_LongReplayDoesNotBlockOrRepublishHistory(t *testing.T) {
	for _, method := range []string{"session/load", "session/resume"} {
		t.Run(method, func(t *testing.T) {
			s, out, in := newTestSession(t, nil)
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			defer cancel()
			s.ctx = ctx
			serverDone := make(chan error, 1)
			go func() {
				dec, enc := json.NewDecoder(in), json.NewEncoder(out)
				for _, expected := range []string{"initialize", method} {
					var req struct {
						ID     json.RawMessage
						Method string
					}
					if err := dec.Decode(&req); err != nil {
						serverDone <- err
						return
					}
					if req.Method != expected {
						serverDone <- fmt.Errorf("method = %s, want %s", req.Method, expected)
						return
					}
					var result any
					if expected == "initialize" {
						result = initializeResult(method == "session/load", method == "session/resume", false)
					} else {
						for i := 0; i < 2048; i++ {
							for _, kind := range []string{"agent_message_chunk", "agent_thought_chunk", "tool_call"} {
								update := map[string]any{"sessionUpdate": kind, "toolCallId": fmt.Sprint(i), "content": map[string]string{"type": "text", "text": "historical answer"}}
								if err := enc.Encode(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "test-session-id", "update": update}}); err != nil {
									serverDone <- err
									return
								}
							}
						}
						// Mode/config observations still need to survive history replay.
						if err := enc.Encode(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "test-session-id", "update": map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "plan"}}}); err != nil {
							serverDone <- err
							return
						}
						result = json.RawMessage(permissionFixture("allowAll")) // no sessionId is valid
					}
					if err := enc.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
						serverDone <- err
						return
					}
				}
				serverDone <- nil
			}()
			if err := s.handshake("test-session-id", ""); err != nil {
				t.Fatal(err)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
			if s.replaying.Load() || len(s.events) != 0 {
				t.Fatalf("replay leaked events or remained active: %d", len(s.events))
			}
			if s.CurrentSessionID() != "test-session-id" || !s.AutoApprovesPermissions() {
				t.Fatal("resume lost session identity or effective policy")
			}
			if s.CurrentMode() != "allowAll" {
				t.Fatal("resume config did not retain the authoritative approval policy")
			}
			s.promptInFlight.Store(1)
			s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"new answer"}}}`))
			if ev := <-s.events; ev.Type != core.EventText || ev.Content != "new answer" {
				t.Fatalf("live event lost: %+v", ev)
			}
		})
	}
}

func TestHandshake_FailedResumeClearsReplayGuard(t *testing.T) {
	tr, _, cancel := startACPScriptedServer(t, []any{initializeResult(true, false, false), nil, map[string]any{"sessionId": "fresh"}})
	defer cancel()
	s := newHandshakeTestSession(tr)
	if err := s.handshake("old", ""); err != nil {
		t.Fatal(err)
	}
	if s.replaying.Load() {
		t.Fatal("failed resume suppressed subsequent live events")
	}
}

func TestMuseBackgroundAnswerWaitsForExplicitCompletion(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.explicitBackgroundCompletion.Store(true)
	for _, text := range []string{"first part", "second part"} {
		s.onNotification("session/update", []byte(fmt.Sprintf(`{"sessionId":"test-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}`, text)))
	}
	if len(s.events) != 2 {
		t.Fatalf("background answer split into premature results: %d events", len(s.events))
	}
	for i := 0; i < 2; i++ {
		if ev := <-s.events; ev.Type != core.EventText {
			t.Fatalf("premature boundary: %+v", ev)
		}
	}
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"turnId":"bg","terminal":"completed"}}}}}`))
	if ev := <-s.events; ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("missing explicit boundary: %+v", ev)
	}
}

func TestHandshake_NegotiatesMuseBackgroundBoundary(t *testing.T) {
	for _, name := range []string{"muse-acp", "scripted-acp"} {
		t.Run(name, func(t *testing.T) {
			init := initializeResult(false, false, false)
			init["agentInfo"] = map[string]any{"name": name}
			tr, _, cancel := startACPScriptedServer(t, []any{init, map[string]any{"sessionId": "new"}})
			defer cancel()
			s := newHandshakeTestSession(tr)
			if err := s.handshake("", ""); err != nil {
				t.Fatal(err)
			}
			if s.explicitBackgroundCompletion.Load() != (name == "muse-acp") {
				t.Fatal("background completion policy does not match negotiated agent")
			}
		})
	}
}
