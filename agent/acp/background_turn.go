package acp

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"log/slog"
)

// backgroundTurnCompletion handles the negotiated Muse ACP v1 extension.
// A host-initiated turn has no pending session/prompt RPC to settle; its
// explicit terminal notification supplies the boundary the engine needs to
// send accumulated answer chunks and persist assistant history.
func backgroundTurnCompletion(sessionID string, params json.RawMessage) ([]core.Event, bool) {
	var wrap struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind string `json:"sessionUpdate"`
			Meta struct {
				Muse struct {
					Completed *struct {
						TurnID   string `json:"turnId"`
						Terminal string `json:"terminal"`
						Detail   string `json:"detail"`
					} `json:"backgroundTurnCompleted"`
				} `json:"muse"`
			} `json:"_meta"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || wrap.Update.Kind != "session_info_update" || wrap.Update.Meta.Muse.Completed == nil {
		return nil, false
	}
	end := wrap.Update.Meta.Muse.Completed
	if wrap.SessionID != sessionID || end.TurnID == "" {
		return nil, true
	}
	if end.Terminal != "completed" && end.Terminal != "cancelled" && end.Terminal != "failed" {
		return nil, true
	}
	slog.Info("acp: background turn complete", "session_id", sessionID, "turn_id", end.TurnID, "terminal", end.Terminal)
	var events []core.Event
	if end.Terminal == "failed" && end.Detail != "" {
		events = append(events, core.Event{Type: core.EventText, SessionID: sessionID, Content: "\n\n" + end.Detail})
	}
	events = append(events, core.Event{Type: core.EventResult, SessionID: sessionID, Done: true})
	return events, true
}
