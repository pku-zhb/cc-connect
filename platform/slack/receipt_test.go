package slack

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	slackapi "github.com/slack-go/slack"
)

type receiptAPI struct {
	mu            sync.Mutex
	eyes          bool
	adds, removes int
	addEntered    chan struct{}
	addRelease    chan struct{}
}

func newReceiptAPI(t *testing.T, api *receiptAPI) *Platform {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("channel") != "C1" || r.Form.Get("timestamp") != "123.456" || r.Form.Get("name") != "eyes" {
			http.Error(w, "wrong reaction target", 400)
			return
		}
		if r.URL.Path == "/reactions.add" && api.addEntered != nil {
			api.addEntered <- struct{}{}
			<-api.addRelease
		}
		api.mu.Lock()
		defer api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/reactions.add":
			api.adds++
			if api.eyes {
				fmt.Fprint(w, `{"ok":false,"error":"already_reacted"}`)
				return
			}
			api.eyes = true
		case "/reactions.remove":
			api.removes++
			api.eyes = false
		default:
			http.Error(w, "unexpected endpoint", 400)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	return &Platform{client: slackapi.New("test", slackapi.OptionAPIURL(server.URL+"/"))}
}

// Regression: eyes already added by the receipt must still be removed when
// processing finishes; already_reacted does not transfer ownership to Slack.
func TestStartTyping_CleansAlreadyReactedEyes(t *testing.T) {
	api := &receiptAPI{eyes: true}
	p := newReceiptAPI(t, api)
	stop := p.StartTyping(context.Background(), replyContext{channel: "C1", msgTS: "123.456"})
	stop()
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.eyes || api.removes != 1 {
		t.Fatalf("completed message retains receipt: %+v", api)
	}
}

func TestReceipt_EarlyAcknowledgementAndTypingShareCleanup(t *testing.T) {
	api := &receiptAPI{}
	p := newReceiptAPI(t, api)
	msg := &core.Message{ReplyCtx: replyContext{channel: "C1", msgTS: "123.456"}}
	p.attachReceipt(msg)
	// Simulate receipt completion before the agent starts.
	msg.ReplyCtx.(replyContext).receipt.acknowledge()
	stop := p.StartTyping(context.Background(), msg.ReplyCtx)
	stop()
	stop()
	p.FinishReceipt(context.Background(), msg.ReplyCtx)
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.eyes || api.adds != 1 || api.removes != 1 {
		t.Fatalf("receipt lifecycle duplicated or leaked: %+v", api)
	}
}

func TestReceipt_LateAcknowledgementCannotOutliveCompletion(t *testing.T) {
	api := &receiptAPI{addEntered: make(chan struct{}, 1), addRelease: make(chan struct{})}
	p := newReceiptAPI(t, api)
	msg := &core.Message{ReplyCtx: replyContext{channel: "C1", msgTS: "123.456"}}
	p.attachReceipt(msg)
	msg.OnAccepted()
	select {
	case <-api.addEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("receipt request did not start")
	}
	done := make(chan struct{})
	go func() { p.FinishReceipt(context.Background(), msg.ReplyCtx); close(done) }()
	close(api.addRelease)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("receipt cleanup did not finish")
	}
	// A callback scheduled after the completed turn must not issue another add.
	msg.ReplyCtx.(replyContext).receipt.acknowledge()
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.eyes || api.adds != 1 || api.removes != 1 {
		t.Fatalf("late receipt leaked: %+v", api)
	}
}

func TestReceipt_CompletionBeforeCallbackPreventsLateAdd(t *testing.T) {
	api := &receiptAPI{}
	p := newReceiptAPI(t, api)
	msg := &core.Message{ReplyCtx: replyContext{channel: "C1", msgTS: "123.456"}}
	p.attachReceipt(msg)
	p.FinishReceipt(context.Background(), msg.ReplyCtx)
	msg.ReplyCtx.(replyContext).receipt.acknowledge()
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.eyes || api.adds != 0 {
		t.Fatal("receipt added after completion")
	}
}
