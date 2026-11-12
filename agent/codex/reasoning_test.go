package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestReasoning_AppServerSwitchesUltraPerTurnWithoutReplacingThread(t *testing.T) {
	a := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, effortSource: a.GetReasoningEffort, pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	w := &tierRPCWriter{session: s}
	s.stdin = w
	if err := s.ensureThread("native-1"); err != nil {
		t.Fatal(err)
	}
	// A saved thread's old effort must not replace the workspace selection.
	old := "ultra"
	s.applyThreadRuntimeState("", "", &old)
	for _, effort := range []string{"ultra", "max", "high", "ultra"} {
		a.SetReasoningEffort(effort)
		if err := s.Send("continue", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		request := w.last()
		params := request["params"].(map[string]any)
		if request["method"] != "turn/start" || params["threadId"] != "native-1" || params["effort"] != effort {
			t.Fatalf("reasoning switch changed the thread or sent stale effort: %#v", request)
		}
	}
}

func TestReasoning_AgentStartWiresLiveSelectionForBothBackends(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"exec", "app_server"} {
		t.Run(backend, func(t *testing.T) {
			raw, err := New(map[string]any{
				"cmd":     []string{bin, "-test.run=^TestDesktopCLIProcess$", "--", "desktop-fixture"},
				"backend": backend, "work_dir": t.TempDir(), "reasoning_effort": "ultra",
				"env": map[string]string{"CC_DESKTOP_CLI_HELPER": "1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			a := raw.(*Agent)
			s, err := a.StartSession(context.Background(), "fixture-thread")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, effort := range []string{"ultra", "max", "high", "ultra"} {
				a.SetReasoningEffort(effort)
				if got := s.(interface{ GetReasoningEffort() string }).GetReasoningEffort(); got != effort {
					t.Fatalf("live %s effort = %q, want %q", backend, got, effort)
				}
				if cs, ok := s.(*codexSession); ok {
					args := strings.Join(cs.buildExecArgs("continue", nil), " ")
					if !strings.Contains(args, fmt.Sprintf("model_reasoning_effort=%q", effort)) || !strings.Contains(args, "resume") || cs.CurrentSessionID() != "fixture-thread" {
						t.Fatalf("resume arguments lost effort or thread: %s", args)
					}
				}
			}
		})
	}
}

func TestReasoning_ConcurrentSelectionsAndNativeDefault(t *testing.T) {
	a := &Agent{}
	s := &appServerSession{effort: "high", effortSource: a.GetReasoningEffort}
	if got := s.GetReasoningEffort(); got != "high" {
		t.Fatalf("inherited effort = %q", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				a.SetReasoningEffort("ultra")
				_ = s.GetReasoningEffort()
				a.SetReasoningEffort("high")
			}
		}()
	}
	wg.Wait()
}
