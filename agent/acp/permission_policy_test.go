package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func permissionFixture(mode string) string {
	return fmt.Sprintf(`{"configOptions":[
{"id":"mode","category":"mode","type":"select","currentValue":"default","options":[{"value":"default"},{"value":"plan"}]},
{"id":"approval_mode","name":"Approval Mode","type":"select","currentValue":%q,"options":[{"value":"allowAll","name":"Allow all"},{"value":"promptUnmatched","name":"Prompt unmatched"},{"value":"onRequest"}]}]}`, mode)
}

func TestPermissionMode_YoloChangesApprovalNotSessionBehavior(t *testing.T) {
	a := &Agent{}
	s, out, in := newTestSession(t, a)
	if err := s.absorbConfigJSON([]byte(permissionFixture("promptUnmatched"))); err != nil {
		t.Fatal(err)
	}
	if a.GetMode() != "promptUnmatched" || len(a.PermissionModes()) != 3 {
		t.Fatal("session behavior shadowed approval mode")
	}
	resolved, ok := a.ResolveMode("yolo")
	if !ok || resolved != "allowAll" {
		t.Fatal("yolo did not resolve to the advertised approval value")
	}
	done := make(chan error, 1)
	go func() {
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct{ ConfigID, Value, SessionID string }
		}
		if err := json.NewDecoder(in).Decode(&request); err != nil {
			done <- err
			return
		}
		if request.Method != "session/set_config_option" || request.Params.ConfigID != "approval_mode" || request.Params.Value != "allowAll" {
			done <- fmt.Errorf("wrong policy RPC: %+v", request)
			_, _ = fmt.Fprintf(out, "{\"id\":%s,\"error\":{\"code\":-32602,\"message\":\"unexpected request\"}}\n", request.ID)
			return
		}
		err := json.NewEncoder(out).Encode(map[string]any{"id": request.ID, "result": json.RawMessage(permissionFixture("allowAll"))})
		done <- err
	}()
	if !s.SetLiveMode("yolo") {
		select {
		case err := <-done:
			t.Fatalf("live yolo was rejected: %v", err)
		default:
			t.Fatal("live yolo was rejected")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !s.AutoApprovesPermissions() || a.GetMode() != "allowAll" || s.configForCategory("mode").CurrentValue != "default" {
		t.Fatal("effective approval policy was not reported correctly")
	}
	a.SetMode("YOLO")
	if a.GetMode() != "allowAll" || a.WorkspaceAgentOptions()["mode"] != "allowAll" {
		t.Fatal("future/workspace agents lost the selected approval policy")
	}
}

func TestPermissionMode_UnconfirmedSwitchDoesNotEnableAutoApproval(t *testing.T) {
	s, out, in := newTestSession(t, nil)
	_ = s.absorbConfigJSON([]byte(permissionFixture("promptUnmatched")))
	go func() {
		var request struct{ ID json.RawMessage }
		_ = json.NewDecoder(in).Decode(&request)
		_ = json.NewEncoder(out).Encode(map[string]any{"id": request.ID, "result": json.RawMessage(permissionFixture("promptUnmatched"))})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.SetConfigOption(ctx, "approval_mode", "allowAll") == nil {
		t.Fatal("unconfirmed mode was reported as applied")
	}
	if s.AutoApprovesPermissions() {
		t.Fatal("unconfirmed selection enabled auto approval")
	}
	if s.SetLiveMode("invented-mode") {
		t.Fatal("unadvertised mode accepted")
	}
}
