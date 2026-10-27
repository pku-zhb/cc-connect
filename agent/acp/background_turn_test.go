package acp

import (
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestBackgroundTurnCompleted_ReleasesBufferedAnswer(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.explicitBackgroundCompletion.Store(true)
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"complete background research report"}}}`))
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"turnId":"background-report","terminal":"completed","stopReason":"end_turn"}}}}}`))
	var events []core.Event
	for {
		select {
		case ev := <-s.events:
			events = append(events, ev)
		default:
			goto drained
		}
	}
drained:
	if len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult || !events[1].Done {
		t.Fatalf("background answer has no final delivery boundary: %+v", events)
	}
	if events[1].SessionID != "test-session-id" {
		t.Fatal("background completion routed to wrong session")
	}
}

func TestBackgroundTurnCompleted_RejectsWrongSessionAndMalformedMetadata(t *testing.T) {
	for _, data := range []string{
		`{"sessionId":"other","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"turnId":"b1","terminal":"completed"}}}}}`,
		`{"sessionId":"test-session-id","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"terminal":"completed"}}}}}`,
		`{"sessionId":"test-session-id","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"turnId":"b1","terminal":"running"}}}}}`,
	} {
		s, _, _ := newTestSession(t, nil)
		s.explicitBackgroundCompletion.Store(true)
		s.onNotification("session/update", []byte(data))
		select {
		case ev := <-s.events:
			t.Fatalf("invalid terminal escaped: %+v", ev)
		default:
		}
	}
}

func TestBackgroundTurnCompleted_FailurePreservesDiagnosticAndCompletion(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	s.explicitBackgroundCompletion.Store(true)
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"session_info_update","_meta":{"muse":{"backgroundTurnCompleted":{"turnId":"b1","terminal":"failed","detail":"background provider failed"}}}}}`))
	first := <-s.events
	last := <-s.events
	if first.Type != core.EventText || first.Content != "\n\nbackground provider failed" || last.Type != core.EventResult {
		t.Fatalf("failure left reader hanging: %+v %+v", first, last)
	}
}
