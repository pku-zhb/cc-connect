package slack

import (
	"context"
	"log/slog"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
)

// messageReceipt owns the early acknowledgement and its cleanup together.
// Serialize the bounded API calls so a slow acknowledgement cannot finish
// after cleanup and leave a permanent eyes reaction on a completed message.
type messageReceipt struct {
	mu              sync.Mutex
	client          *slackapi.Client
	ref             slackapi.ItemRef
	added, finished bool
}

func (r *messageReceipt) acknowledge() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished || r.added {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.client.AddReactionContext(ctx, "eyes", r.ref)
	if err == nil || err.Error() == "already_reacted" {
		r.added = true
		return
	}
	slog.Warn("slack: receipt reaction failed", "channel", r.ref.Channel, "ts", r.ref.Timestamp, "error", err)
}

func (r *messageReceipt) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Also try after an add timeout: Slack may have accepted the request
	// even when its response did not reach us.
	if err := r.client.RemoveReactionContext(ctx, "eyes", r.ref); err != nil && err.Error() != "no_reaction" {
		slog.Warn("slack: receipt cleanup failed", "channel", r.ref.Channel, "ts", r.ref.Timestamp, "error", err)
	}
}

// FinishReceipt also covers failure before StartTyping and dropped queue items.
func (*Platform) FinishReceipt(_ context.Context, rctx any) {
	if rc, ok := rctx.(replyContext); ok && rc.receipt != nil {
		rc.receipt.finish()
	}
}
