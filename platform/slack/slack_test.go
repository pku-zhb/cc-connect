package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/chenhg5/cc-connect/core"
)

func TestStripAppMentionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips bot mention prefix",
			in:   "<@U0BOT123> run tests",
			want: "run tests",
		},
		{
			name: "empty mention becomes empty text",
			in:   "<@U0BOT123> ",
			want: "",
		},
		{
			name: "plain text remains unchanged",
			in:   "run tests",
			want: "run tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAppMentionText(tt.in); got != tt.want {
				t.Fatalf("stripAppMentionText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReplyContextReactionTS(t *testing.T) {
	tests := []struct {
		name string
		rc   replyContext
		want string
	}{
		{
			name: "mention inside a thread targets the mention, not the root",
			rc:   replyContext{channel: "C1", timestamp: "1700000000.000100", msgTS: "1700000500.000200"},
			want: "1700000500.000200",
		},
		{
			name: "top-level mention where root and message coincide",
			rc:   replyContext{channel: "C1", timestamp: "1700000000.000100", msgTS: "1700000000.000100"},
			want: "1700000000.000100",
		},
		{
			name: "reconstructed context (cron, send-to-session) falls back to the thread root",
			rc:   replyContext{channel: "C1", timestamp: "1700000000.000100"},
			want: "1700000000.000100",
		},
		{
			name: "slash-command context has no reaction target",
			rc:   replyContext{channel: "C1"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rc.reactionTS(); got != tt.want {
				t.Fatalf("reactionTS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadSlackFile_HTMLDetection(t *testing.T) {
	// Test that we detect HTML responses (Slack login page) and return an error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate Slack returning HTML login page when auth is missing
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<!DOCTYPE html><html><body>Please login</body></html>"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for HTML response, got nil")
	}
	// Should detect HTML prefix
	if err != nil && err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestDownloadSlackFile_MissingAuth(t *testing.T) {
	// Test that we return an error for non-200 status codes
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestDownloadSlackFile_Success(t *testing.T) {
	// Test successful binary download
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header is set
		auth := r.Header.Get("Authorization")
		if auth != "Bearer xoxb-test-token" {
			t.Errorf("expected Authorization header 'Bearer xoxb-test-token', got %q", auth)
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("\x89PNG\r\n\x1a\n")) // PNG magic bytes
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	data, err := p.downloadSlackFile(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 8 {
		t.Errorf("expected 8 bytes, got %d", len(data))
	}
}

func TestDownloadSlackFile_EmptyURL(t *testing.T) {
	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile("")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestParseSlackInnerEventFiles(t *testing.T) {
	raw := json.RawMessage(`{"type":"app_mention","user":"U1","text":"<@B> hi","files":[{"id":"F1","name":"a.pdf","mimetype":"application/pdf","url_private_download":"http://example/f"}]}`)
	files := parseSlackInnerEventFiles(&raw)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if files[0].Name != "a.pdf" || files[0].Mimetype != "application/pdf" {
		t.Fatalf("unexpected file: %+v", files[0])
	}
}

func TestProcessSlackFileShares_GenericFile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("%PDF-1.4 minimal"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{
			ID:                 "Fpdf",
			Name:               "doc.pdf",
			Mimetype:           "application/pdf",
			URLPrivateDownload: ts.URL,
		},
	})
	if len(images) != 0 || audio != nil {
		t.Fatalf("expected only doc file, got images=%d audio=%v", len(images), audio)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].FileName != "doc.pdf" || docs[0].MimeType != "application/pdf" {
		t.Fatalf("unexpected doc: %+v", docs[0])
	}
	if string(docs[0].Data) != "%PDF-1.4 minimal" {
		t.Fatalf("unexpected data %q", docs[0].Data)
	}
}

func TestProcessSlackFileShares_ImageVsDoc(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fakepng"))
	}))
	defer imgSrv.Close()
	txtSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer txtSrv.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "1", Name: "x.png", Mimetype: "image/png", URLPrivateDownload: imgSrv.URL},
		{ID: "2", Name: "n.txt", Mimetype: "text/plain", URLPrivateDownload: txtSrv.URL},
	})
	if audio != nil {
		t.Fatal("unexpected audio")
	}
	if len(images) != 1 || len(docs) != 1 {
		t.Fatalf("want 1 image 1 doc, got images=%d docs=%d", len(images), len(docs))
	}
	if images[0].MimeType != "image/png" {
		t.Errorf("image mime: %q", images[0].MimeType)
	}
	if docs[0].MimeType != "text/plain" || string(docs[0].Data) != "hello" {
		t.Errorf("unexpected text file: %+v", docs[0])
	}
}

func TestProcessSlackFileShares_EmptyMimeBecomesOctetStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0, 1, 2})
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	_, _, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "z", Name: "blob.bin", Mimetype: "", URLPrivateDownload: ts.URL},
	})
	if len(docs) != 1 || docs[0].MimeType != "application/octet-stream" {
		t.Fatalf("got %+v", docs)
	}
}

// newDedupTestPlatform returns a Platform wired for handleEvent tests: the name
// caches are pre-seeded so resolveUserName / resolveChannelNameForMsg never
// reach for the (nil) API client.
func newDedupTestPlatform(t *testing.T) (*Platform, *[]*core.Message) {
	t.Helper()
	var got []*core.Message
	p := &Platform{
		allowFrom:        "*",
		sessionScope:     "channel",
		channelNameCache: map[string]string{"C1": "product"},
	}
	p.userNameCache.Store("U1", "Jim")
	p.handler = func(_ core.Platform, msg *core.Message) { got = append(got, msg) }
	return p, &got
}

// freshTS returns a Slack ts that is newer than core.StartTime, so the adapter's
// post-restart old-message filter does not drop the event before the dedup guard.
func freshTS(seq int) string {
	return fmt.Sprintf("%d.%06d", time.Now().Add(time.Minute).Unix(), seq)
}

// mentionEvent builds the socketmode event Slack delivers for the app_mention
// subscription; messageEvent builds the one it delivers for message.channels.
// A message that @-mentions the bot produces both.
func mentionEvent(channel, user, ts, text string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.AppMentionEvent{
					User: user, Channel: channel, TimeStamp: ts, Text: text,
				},
			},
		},
	}
}

func messageEvent(channel, user, ts, text string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{
					User: user, Channel: channel, TimeStamp: ts, Text: text,
				},
			},
		},
	}
}

// A message that mentions the bot arrives twice — once as app_mention, once as
// message. Before the dedup guard both were dispatched: the first started a
// turn and the second hit the still-busy session, so every mention drew a
// "message queued" reply and burned a second turn.
func TestHandleEvent_MentionDeliveredTwiceDispatchesOnce(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	ts := freshTS(1)
	text := "a quick intro to <@UBOT> for the team"

	p.handleEvent(mentionEvent("C1", "U1", ts, text))
	p.handleEvent(messageEvent("C1", "U1", ts, text))

	if len(*got) != 1 {
		t.Fatalf("dispatched %d times, want 1", len(*got))
	}
	if (*got)[0].MessageID != ts {
		t.Errorf("MessageID = %q, want %q", (*got)[0].MessageID, ts)
	}
}

// Order must not matter: Slack does not guarantee which subscription lands first.
func TestHandleEvent_MentionDedupIsOrderIndependent(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	ts := freshTS(1)

	p.handleEvent(messageEvent("C1", "U1", ts, "hello <@UBOT>"))
	p.handleEvent(mentionEvent("C1", "U1", ts, "hello <@UBOT>"))

	if len(*got) != 1 {
		t.Fatalf("dispatched %d times, want 1", len(*got))
	}
}

// The guard must key on the message, not the channel: distinct messages still
// get through, including the same ts seen in two different channels.
func TestHandleEvent_DistinctMessagesStillDispatch(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	p.channelNameCache["C2"] = "other"

	p.handleEvent(messageEvent("C1", "U1", freshTS(1), "first"))
	p.handleEvent(messageEvent("C1", "U1", freshTS(2), "second"))
	p.handleEvent(messageEvent("C2", "U1", freshTS(1), "same ts, other channel"))

	if len(*got) != 3 {
		t.Fatalf("dispatched %d times, want 3", len(*got))
	}
}

func TestDedupKey(t *testing.T) {
	if got := dedupKey("C1", "123.456"); got != "C1:123.456" {
		t.Errorf("dedupKey = %q, want %q", got, "C1:123.456")
	}
	// An empty ts must produce an empty key — core.MessageDedup never treats
	// the empty string as a duplicate, so such events are never swallowed.
	if got := dedupKey("C1", ""); got != "" {
		t.Errorf("dedupKey with empty ts = %q, want empty", got)
	}
}

func TestHandleEvent_BotAllowFrom(t *testing.T) {
	for _, source := range []string{"message", "app_mention"} {
		for _, tc := range []struct {
			name, allow, user, bot string
			want                   bool
		}{
			{"default excludes bots", "", "U1", "B1", false},
			{"wildcard excludes bots", "*", "U1", "B1", false},
			{"human identity cannot bypass bot check", "U1", "U1", "B1", false},
			{"explicit bot", "U2,B1", "U1", "B1", true},
			{"bot without user", "B1", "", "B1", true},
			{"case insensitive", " b1 ", "U1", "B1", true},
			{"unknown bot", "*,B2", "U1", "B1", false},
			{"own bot", "BSELF", "USELF", "BSELF", false},
			{"own user", "*", "USELF", "", false},
			{"human with mixed wildcard", "*,B1", "U1", "", true},
			{"unauthorized human", "B1", "U1", "", false},
			{"missing sender", "*", "", "", false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				p, got := newDedupTestPlatform(t)
				p.allowFrom, p.selfBotID, p.selfUserID = tc.allow, "BSELF", "USELF"
				evt := messageEvent("C1", tc.user, freshTS(1), "ping")
				if source == "app_mention" {
					evt = mentionEvent("C1", tc.user, freshTS(1), "ping")
				}
				data := evt.Data.(slackevents.EventsAPIEvent)
				switch ev := data.InnerEvent.Data.(type) {
				case *slackevents.MessageEvent:
					ev.BotID = tc.bot
				case *slackevents.AppMentionEvent:
					ev.BotID = tc.bot
				}
				p.handleEvent(evt)
				if (len(*got) == 1) != tc.want {
					t.Fatalf("dispatched %d messages, want allowed=%v", len(*got), tc.want)
				}
				if tc.want && tc.user == "" && (*got)[0].UserID != tc.bot {
					t.Fatal("bot sender identity was lost")
				}
			})
		}
	}
}

func TestAllowSender_LogsIgnoredBotWithoutBody(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{allowFrom: "*"}
	if p.allowSender("U1", "B1", "message", "C1", "123.456") {
		t.Fatal("unexpectedly allowed bot")
	}
	for _, field := range []string{`"reason":"bot_not_allowlisted"`, `"bot_id":"B1"`, `"user":"U1"`, `"channel":"C1"`, `"ts":"123.456"`} {
		if !strings.Contains(buf.String(), field) {
			t.Errorf("missing %s in log: %s", field, buf.String())
		}
	}
}

func TestAcceptedReceiptBeforeAgentStartup(t *testing.T) {
	for _, mention := range []bool{false, true} {
		t.Run(fmt.Sprintf("mention=%v", mention), func(t *testing.T) {
			calls := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				calls <- r.URL.Path + ":" + r.Form.Get("channel") + ":" + r.Form.Get("timestamp") + ":" + r.Form.Get("name")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			p, messages := newDedupTestPlatform(t)
			p.client = slackapi.New("test", slackapi.OptionAPIURL(server.URL+"/"))
			ts := freshTS(101)
			event := messageEvent("C1", "U1", ts, "hello")
			if mention {
				event = mentionEvent("C1", "U1", ts, "<@UBOT> hello")
			}
			p.handleEvent(event)
			if len(*messages) != 1 || (*messages)[0].OnAccepted == nil {
				t.Fatal("missing pre-start receipt callback")
			}
			select {
			case <-calls:
				t.Fatal("receipt sent before acceptance")
			default:
			}
			// Startup may now block indefinitely; acceptance must still react.
			(*messages)[0].OnAccepted()
			select {
			case got := <-calls:
				if want := "/reactions.add:C1:" + ts + ":eyes"; got != want {
					t.Fatalf("receipt=%q want=%q", got, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no receipt before startup")
			}
			p.handleEvent(event)
			if len(*messages) != 1 {
				t.Fatal("duplicate delivered twice")
			}
		})
	}
}
