package antigravity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/chenhg5/cc-connect/core"
)

func (as *antigravitySession) usesStreamJSON() bool {
	for i, arg := range as.extraArgs {
		if arg == "--output-format=stream-json" || (arg == "--output-format" && i+1 < len(as.extraArgs) && as.extraArgs[i+1] == "stream-json") {
			return true
		}
	}
	return false
}

// Modern agy supplies conversation IDs in its native stream instead of the
// legacy .gemini/tmp chat files. Keep its wire JSON out of platform messages.
// It returns as soon as the result event arrives; resultSeen reports whether
// the turn reached a result (even an unsuccessful one).
func (as *antigravitySession) readStreamJSON(reader io.Reader) (resultSeen bool, err error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	hasText := false
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var event struct {
			Event          string `json:"event"`
			ConversationID string `json:"conversation_id"`
			Step           struct {
				ConversationID string `json:"conversation_id"`
				Type           string `json:"step_type"`
				State          string `json:"state"`
				Text           string `json:"text_delta"`
				ToolName       string `json:"tool_name"`
				ToolInfo       struct {
					Parameters json.RawMessage `json:"parameters"`
				} `json:"tool_info"`
			} `json:"step_update"`
			Result struct {
				ConversationID string `json:"conversation_id"`
				Status         string `json:"status"`
				Response       string `json:"response"`
			} `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return false, fmt.Errorf("antigravity: decode stream event: %w", err)
		}
		sid := event.ConversationID
		if sid == "" {
			sid = event.Step.ConversationID
		}
		if sid == "" {
			sid = event.Result.ConversationID
		}
		if sid != "" && sid != as.CurrentSessionID() {
			as.chatID.Store(sid)
			if err := as.emitStreamEvent(core.Event{Type: core.EventText, SessionID: sid}); err != nil {
				return false, err
			}
		}
		var output core.Event
		switch event.Event {
		case "step_update":
			switch {
			case event.Step.Type == "agent_response" && event.Step.Text != "":
				hasText = true
				output = core.Event{Type: core.EventText, Content: event.Step.Text}
			case event.Step.Type == "tool" && event.Step.State == "ACTIVE":
				output = core.Event{Type: core.EventToolUse, ToolName: event.Step.ToolName, ToolInput: string(event.Step.ToolInfo.Parameters)}
			}
		case "result":
			if event.Result.Status != "SUCCESS" {
				return true, fmt.Errorf("antigravity: turn ended with status %q: %s", event.Result.Status, event.Result.Response)
			}
			if !hasText && event.Result.Response != "" {
				if err := as.emitStreamEvent(core.Event{Type: core.EventText, Content: event.Result.Response}); err != nil {
					return true, err
				}
			}
			return true, nil
		}
		if output.Type != "" {
			if err := as.emitStreamEvent(output); err != nil {
				return false, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("antigravity: read stream: %w", err)
	}
	return false, fmt.Errorf("antigravity: stream ended without a result")
}

func (as *antigravitySession) emitStreamEvent(event core.Event) error {
	select {
	case as.events <- event:
		return nil
	case <-as.ctx.Done():
		return as.ctx.Err()
	}
}
