package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Capture real JSON-RPC requests at the subprocess boundary and reply without
// running a model. All request construction remains production code.
type tierRPCWriter struct {
	session  *appServerSession
	mu       sync.Mutex
	requests []map[string]any
}

func (w *tierRPCWriter) Close() error { return nil }
func (w *tierRPCWriter) Write(b []byte) (int, error) {
	var req map[string]any
	if err := json.Unmarshal(b, &req); err != nil {
		return 0, err
	}
	w.mu.Lock()
	w.requests = append(w.requests, req)
	w.mu.Unlock()
	id, _ := rpcIDToInt64(req["id"])
	params := req["params"].(map[string]any)
	result := map[string]any{"turn": map[string]any{"id": "turn-1"}}
	if req["method"] == "thread/start" || req["method"] == "thread/resume" {
		tier := params["serviceTier"]
		if tier == nil {
			tier = "priority"
		} // inherited native Fast default
		result = map[string]any{"thread": map[string]any{"id": "native-1"}, "serviceTier": tier}
	}
	raw, _ := json.Marshal(result)
	w.session.handleResponse(rpcResponseEnvelope{ID: id, Result: raw})
	return len(b), nil
}
func (w *tierRPCWriter) last() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requests[len(w.requests)-1]
}

func TestServiceTier_AppServerPreservesThreadAndAppliesWorkspaceChoicePerTurn(t *testing.T) {
	a := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, tierSource: a.configuredServiceTier, pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	w := &tierRPCWriter{session: s}
	s.stdin = w
	if err := s.ensureThread(""); err != nil {
		t.Fatal(err)
	}
	if tier, err := s.GetServiceTier(ctx); err != nil || tier != "priority" {
		t.Fatalf("inherited tier = %q, %v", tier, err)
	}
	for _, tier := range []string{"default", "fast", "default"} {
		a.SetServiceTier(tier)
		if err := s.Send("hello", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		params := w.last()["params"].(map[string]any)
		if params["serviceTier"] != normalizeServiceTier(tier) {
			t.Fatalf("request = %#v", params)
		}
		if params["threadId"] != "native-1" {
			t.Fatalf("thread lost: %#v", params)
		}
	}
	if err := s.ensureThread("native-1"); err != nil {
		t.Fatal(err)
	}
	if w.last()["method"] != "thread/resume" || w.last()["params"].(map[string]any)["serviceTier"] != "default" {
		t.Fatal("resume dropped explicit standard tier")
	}
}

func TestServiceTier_ExecNewAndResumeExplicitStandardOverridesNativeFast(t *testing.T) {
	a := &Agent{}
	cs, err := newCodexSession(context.Background(), "", nil, t.TempDir(), "", "", "", "", "", nil, "", "", "", a.configuredServiceTier)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if strings.Contains(strings.Join(cs.buildExecArgs("", nil), " "), "service_tier") {
		t.Fatal("unset tier should inherit")
	}
	for _, id := range []string{"", "native-1"} {
		if id != "" {
			cs.threadID.Store(id)
		}
		for _, tier := range []string{"fast", "default"} {
			a.SetServiceTier(tier)
			args := strings.Join(cs.buildExecArgs("", nil), " ")
			if !strings.Contains(args, fmt.Sprintf("service_tier=%q", normalizeServiceTier(tier))) {
				t.Fatalf("args = %s", args)
			}
			if id != "" && !strings.Contains(args, "resume") {
				t.Fatal("lost resume")
			}
		}
	}
}

func TestServiceTier_ResolvesNativeConfigAndPreservesWorkspaceOptions(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
 *'"method":"initialize"'*) printf '{"id":1,"result":{}}\n';;
 *'"method":"config/read"'*) printf '{"id":2,"result":{"config":{"service_tier":"fast"}}}\n';;
 esac
done
`
	ps := `while (($line = [Console]::In.ReadLine()) -ne $null) {
 if ($line -like '*"method":"initialize"*') { [Console]::Out.WriteLine('{"id":1,"result":{}}') }
 elseif ($line -like '*"method":"config/read"*') { [Console]::Out.WriteLine('{"id":2,"result":{"config":{"service_tier":"fast"}}}') }
}`
	writeFakeCodexScript(t, dir, script, ps)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, err := New(map[string]any{"cmd": "codex", "work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Agent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if tier, err := a.GetServiceTier(ctx); err != nil || tier != "priority" {
		t.Fatalf("native tier = %q, %v", tier, err)
	}
	a.SetServiceTier("default")
	opts := a.WorkspaceAgentOptions()
	clone, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if tier, err := clone.(*Agent).GetServiceTier(ctx); err != nil || tier != "default" {
		t.Fatalf("clone tier = %q, %v", tier, err)
	}
	if _, err := New(map[string]any{"service_tier": "bad"}); err == nil {
		t.Fatal("invalid tier accepted")
	}
}

func TestServiceTier_ConcurrentSelectionsAndTurnSnapshots(t *testing.T) {
	a := &Agent{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				a.SetServiceTier("fast")
				_ = selectedServiceTier(a.configuredServiceTier)
				a.SetServiceTier("default")
			}
		}()
	}
	wg.Wait()
}
