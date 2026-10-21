package acp

import (
	"context"
	"github.com/chenhg5/cc-connect/core"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type backgroundRecordingPlatform struct {
	mu   sync.Mutex
	sent []string
}

func (*backgroundRecordingPlatform) Name() string                    { return "background-test" }
func (*backgroundRecordingPlatform) Start(core.MessageHandler) error { return nil }
func (*backgroundRecordingPlatform) Stop() error                     { return nil }
func (p *backgroundRecordingPlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, text)
	return nil
}
func (p *backgroundRecordingPlatform) Send(ctx context.Context, r any, text string) error {
	return p.Reply(ctx, r, text)
}

// Run against the real native adapter and its deterministic MSP fixture, without
// model traffic or external messages. This exercises the complete late-answer
// path: host -> adapter -> ACP client -> engine -> platform and saved history.
func TestBackgroundDelivery_RealAdapterAndEngine(t *testing.T) {
	binary, host := os.Getenv("MUSE_ACP_TEST_BINARY"), os.Getenv("MUSE_ACP_TEST_HOST")
	if binary == "" || host == "" {
		t.Skip("set MUSE_ACP_TEST_BINARY and MUSE_ACP_TEST_HOST for native adapter integration")
	}
	dir := t.TempDir()
	agent, err := New(map[string]any{"cmd": binary, "work_dir": dir, "env": map[string]string{
		"MUSE_CLI": host, "FAKE_SCENARIO": "background_completion", "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_STATE_HOME": filepath.Join(dir, "state"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	platform := &backgroundRecordingPlatform{}
	store := filepath.Join(dir, "sessions.json")
	engine := core.NewEngine("background-test", agent, []core.Platform{platform}, store, core.LangEnglish)
	defer engine.Stop()
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	engine.ReceiveMessage(platform, &core.Message{SessionKey: "background-test:thread-1:user-1", MessageID: "launch-1", UserID: "user-1", Content: "launch research", ReplyCtx: "thread-1"})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(store)
		platform.mu.Lock()
		count := 0
		for _, text := range platform.sent {
			if strings.Contains(text, "complete background research report") {
				count++
			}
		}
		platform.mu.Unlock()
		if count > 1 {
			t.Fatal("duplicate terminal delivered report twice")
		}
		if count == 1 && strings.Contains(string(data), "complete background research report") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	t.Fatalf("background answer missing from delivery or history: %v", platform.sent)
}
