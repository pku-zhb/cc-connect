package antigravity

import (
	"context"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"os"
	"strings"
	"testing"
	"time"
)

func TestModernAgyStreamPreservesSessionIDAndPlainText(t *testing.T) {
	if os.Getenv("CC_AGY_STREAM_HELPER") == "1" {
		fmt.Println(`{"event":"init","conversation_id":"native-conversation"}`)
		fmt.Println(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"hello"}}`)
		fmt.Println(`{"event":"result","result":{"conversation_id":"native-conversation","status":"SUCCESS","response":"hello"}}`)
		os.Exit(0)
	}
	s, err := newAntigravitySession(context.Background(), os.Args[0], []string{"-test.run=TestModernAgyStreamPreservesSessionIDAndPlainText", "--", "--output-format", "stream-json"}, t.TempDir(), "", "yolo", "", []string{"CC_AGY_STREAM_HELPER=1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Type == core.EventError {
				t.Fatal(ev.Error)
			}
			if ev.Type == core.EventText {
				text.WriteString(ev.Content)
			}
			if ev.Type == core.EventResult && ev.Done {
				if s.CurrentSessionID() != "native-conversation" {
					t.Fatalf("conversation ID=%q; modern agy has no legacy chat file", s.CurrentSessionID())
				}
				if text.String() != "hello" {
					t.Fatalf("text=%q, want plain text without wire JSON", text.String())
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

// Modern agy writes its conversations outside the legacy .gemini/tmp store.
// The native stream must carry the conversation ID without scanning that store.
func TestStreamJSONPreservesConversationIDWithoutLegacyChatFiles(t *testing.T) {
	s, err := newAntigravitySession(context.Background(), "echo", []string{"--output-format", "stream-json"}, t.TempDir(), "", "yolo", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input := `{"event":"init","conversation_id":"native-conversation"}
{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"read_file","tool_info":{"parameters":{"Path":"probe.txt"}}}}
{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"read_file"}}
{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"hello"}}
{"event":"result","result":{"conversation_id":"native-conversation","status":"SUCCESS","response":"hello"}}
`
	if err := s.readStreamJSON(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "native-conversation" {
		t.Fatalf("conversation ID=%q", s.CurrentSessionID())
	}
	var text string
	tools := 0
	for len(s.events) > 0 {
		ev := <-s.events
		if ev.Type == core.EventText {
			text += ev.Content
		}
		if ev.Type == core.EventToolUse {
			tools++
		}
	}
	if text != "hello" || tools != 1 {
		t.Fatalf("text=%q tools=%d", text, tools)
	}
}

func TestStreamJSONResultFallbackAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{"result only", `{"event":"result","result":{"conversation_id":"id","status":"SUCCESS","response":"fallback"}}`, "fallback", false},
		{"failure", `{"event":"result","result":{"status":"ERROR","response":"request failed"}}`, "", true},
		{"invalid JSON", `not JSON`, "", true},
		{"missing result", `{"event":"init","conversation_id":"id"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newAntigravitySession(context.Background(), "echo", []string{"--output-format=stream-json"}, t.TempDir(), "", "yolo", "", nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			err = s.readStreamJSON(strings.NewReader(tc.input))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			var text string
			for len(s.events) > 0 {
				ev := <-s.events
				if ev.Type == core.EventText {
					text += ev.Content
				}
			}
			if text != tc.want {
				t.Fatalf("text=%q want=%q", text, tc.want)
			}
		})
	}
}

func TestNewStreamJSONOption(t *testing.T) {
	a, err := New(map[string]any{"cmd": "echo", "output_format": "stream-json"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).cliExtraArgs; strings.Join(got, " ") != "--output-format stream-json" {
		t.Fatalf("args=%v", got)
	}
	if _, err := New(map[string]any{"cmd": "echo", "output_format": "json"}); err == nil {
		t.Fatal("expected unsupported format error")
	}
}
