package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const configFixture = `{"configOptions":[
 {"id":"engine-choice","name":"Engine","category":"model","type":"select","currentValue":"m1","options":[{"group":"recommended","name":"Models","options":[{"value":"m1","name":"First"},{"value":"m2","name":"Second"}]}]},
 {"id":"budget","name":"Thinking","category":"thought_level","type":"select","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]},
 {"id":"future","type":"unknown","currentValue":true}
]}`

func TestSessionConfig_ParsesGroupedOptionsAndReturnsCopies(t *testing.T) {
	s := &acpSession{}
	if err := s.absorbConfigJSON([]byte(configFixture)); err != nil {
		t.Fatal(err)
	}
	options := s.ConfigOptions()
	if len(options) != 2 || len(options[0].Options) != 2 || s.GetModel() != "m1" || s.GetReasoningEffort() != "high" {
		t.Fatalf("bad options: %+v", options)
	}
	options[0].Options[0].Value = "changed"
	if s.ConfigOptions()[0].Options[0].Value != "m1" {
		t.Fatal("caller changed the cache")
	}
}

func TestSessionConfig_SetUsesStandardRPCAndAuthoritativeResponse(t *testing.T) {
	s, out, in := newTestSession(t, nil)
	if err := s.absorbConfigJSON([]byte(configFixture)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var req struct {
			ID     json.RawMessage
			Method string
			Params struct{ SessionID, ConfigID, Value string }
		}
		if err := json.NewDecoder(in).Decode(&req); err != nil {
			done <- err
			return
		}
		if req.Method != "session/set_config_option" || req.Params.SessionID != "test-session-id" || req.Params.ConfigID != "engine-choice" || req.Params.Value != "m2" {
			done <- fmt.Errorf("wrong request: %+v", req)
			return
		}
		_, err := fmt.Fprintf(out, "{\"id\":%s,\"result\":{\"configOptions\":[{\"id\":\"engine-choice\",\"category\":\"model\",\"type\":\"select\",\"currentValue\":\"m2\",\"options\":[{\"value\":\"m2\"}]}]}}\n", req.ID)
		done <- err
	}()
	if err := s.SetConfigOption(context.Background(), "engine-choice", "m2"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.GetModel() != "m2" || s.GetReasoningEffort() != "" {
		t.Fatal("response must replace the entire list")
	}
}

func TestSessionConfig_RejectsInvalidAndDoesNotUpdateOnRPCError(t *testing.T) {
	s, out, in := newTestSession(t, nil)
	_ = s.absorbConfigJSON([]byte(configFixture))
	if err := s.SetConfigOption(context.Background(), "engine-choice", "invented"); err == nil {
		t.Fatal("invalid value accepted")
	}
	go func() {
		var req struct{ ID json.RawMessage }
		_ = json.NewDecoder(in).Decode(&req)
		_, _ = fmt.Fprintf(out, "{\"id\":%s,\"error\":{\"code\":-32602,\"message\":\"refused\"}}\n", req.ID)
	}()
	if err := s.SetConfigOption(context.Background(), "engine-choice", "m2"); err == nil {
		t.Fatal("RPC failure hidden")
	}
	if s.GetModel() != "m1" {
		t.Fatal("failed selection changed cache")
	}
}

func TestSessionControls_NotificationsNeverBecomeConversationText(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	update := `{"sessionId":"test-session-id","update":{"sessionUpdate":"config_option_update",` + configFixture[1:]
	s.onNotification("session/update", []byte(update+"}"))
	if s.GetModel() != "m1" {
		t.Fatal("config update ignored")
	}
	s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":{"sessionUpdate":"usage_update","used":0,"size":1000,"cost":{"amount":0,"currency":"USD"}}}`))
	u := s.GetSessionUsage()
	if u == nil || u.Used == nil || *u.Used != 0 || u.Cost == nil || u.Cost.Amount != 0 {
		t.Fatalf("reported zero lost: %+v", u)
	}
	u.Cost.Amount = 99
	if s.GetSessionUsage().Cost.Amount != 0 {
		t.Fatal("caller changed usage")
	}
	s.onNotification("session/update", []byte(`{"sessionId":"other","update":{"sessionUpdate":"usage_update","used":900,"size":1000}}`))
	if s.GetContextUsage().UsedTokens != 0 {
		t.Fatal("cross-session usage leak")
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("metadata became event: %+v", ev)
	case <-time.After(10 * time.Millisecond):
	}
}

func TestSessionUsage_MissingDoesNotInventZero(t *testing.T) {
	s := &acpSession{}
	s.absorbControlUpdate([]byte(`{"update":{"sessionUpdate":"usage_update","_meta":{"vendor":true}}}`))
	if s.GetSessionUsage() != nil || s.GetContextUsage() != nil {
		t.Fatal("metadata-only frame invented usage")
	}
}
