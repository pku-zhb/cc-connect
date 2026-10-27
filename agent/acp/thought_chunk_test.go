package acp

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Muse sends ACP thought deltas, not the similarly named vendor thinking kind.
// Treating these as answer text glued the English summary to the Chinese final.
func TestMapSessionUpdate_MuseThoughtChunkNeverBecomesAnswerText(t *testing.T) {
	for _, messageID := range []string{"", "thought-1"} {
		t.Run("messageId="+messageID, func(t *testing.T) {
			params, _ := json.Marshal(map[string]any{
				"sessionId": "muse-session",
				"update": map[string]any{
					"sessionUpdate": "agent_thought_chunk", "messageId": messageID,
					"content": map[string]string{"type": "text", "text": "Quantifying per-core memory bandwidth."},
				},
			})
			events := mapSessionUpdate("", params)
			if len(events) != 1 || events[0].Type != core.EventThinking {
				t.Fatalf("thought mapped to answer: %+v", events)
			}
			if events[0].Content != "Quantifying per-core memory bandwidth." || events[0].SessionID != "muse-session" {
				t.Fatalf("thought content or session changed: %+v", events[0])
			}
			if events[0].Metadata["thinking_delta"] != true || events[0].Metadata["thinking_id"] != messageID {
				t.Fatalf("thought stream identity lost: %+v", events[0].Metadata)
			}
		})
	}
}

func TestMapSessionUpdate_MuseThoughtWhitespaceAndAnswerStayDistinct(t *testing.T) {
	for _, text := range []string{" ", "\n\n", "下一段摘要。"} {
		params, _ := json.Marshal(map[string]any{"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": text},
		}})
		events := mapSessionUpdate("s1", params)
		if len(events) != 1 || events[0].Type != core.EventThinking || events[0].Content != text {
			t.Fatalf("thought delta lost: %q -> %+v", text, events)
		}
	}
	final := mapSessionUpdate("s1", json.RawMessage(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"最终答案。"}}}`))
	if len(final) != 1 || final[0].Type != core.EventText || final[0].Metadata["thinking_delta"] != nil {
		t.Fatalf("final answer classified as thought: %+v", final)
	}
}
