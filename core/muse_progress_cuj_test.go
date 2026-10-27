package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Extend CUJ-I12 with Muse's incremental thoughts across three real follow-ups.
func TestCUJ_I12_MuseThoughtDeltasAcrossFollowups(t *testing.T) {
	p := &cujSeparatedPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	engine := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer engine.Stop()
	engine.SetDisplayConfig(DisplayCfg{Mode: "full", ThinkingMessages: true, ToolMessages: false})
	for turn := 1; turn <= 3; turn++ {
		final := fmt.Sprintf("独立答案 %d", turn)
		events := []Event{
			{Type: EventThinking, Content: "Checking", Metadata: map[string]any{"thinking_delta": true}},
			{Type: EventThinking, Content: " sources.", Metadata: map[string]any{"thinking_delta": true}},
			{Type: EventThinking, Content: "\n\nChecking methodology.", Metadata: map[string]any{"thinking_delta": true}},
			{Type: EventToolUse, ToolName: "Search"},
			{Type: EventToolResult, Content: "source data"},
			{Type: EventThinking, Content: "Comparing results.", Metadata: map[string]any{"thinking_delta": true}},
			{Type: EventText, Content: final},
			{Type: EventResult, Done: true},
		}
		agent.mu.Lock()
		if len(agent.sessions) == 0 {
			agent.nextSessionEvents = events
		} else {
			s := agent.sessions[len(agent.sessions)-1]
			s.mu.Lock()
			s.pendingEvents = events
			s.mu.Unlock()
		}
		agent.mu.Unlock()
		engine.ReceiveMessage(p, &Message{SessionKey: "test:thread", Platform: "test", UserID: "U1", MessageID: fmt.Sprintf("m%d", turn), Content: "follow up", ReplyCtx: "ctx"})
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			sent := p.getSent()
			if len(sent) >= 2*turn && !engine.sessions.GetOrCreateActive("test:thread").Busy() {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		sent := p.getSent()
		if len(sent) != 2*turn || sent[len(sent)-1] != "FINAL: "+final {
			t.Fatalf("turn %d missing or duplicated final: %v", turn, sent)
		}
		progress := sent[len(sent)-2]
		if progress != "PROGRESS: 💭 Checking sources.\n\n💭 Checking methodology.\n\n💭 Comparing results." || strings.Contains(progress, final) {
			t.Fatalf("turn %d has broken thought stream: %q", turn, progress)
		}
	}
}
