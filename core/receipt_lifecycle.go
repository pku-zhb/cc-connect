package core

import (
	"context"
	"time"
)

func (e *Engine) finishMessageReceipt(platform Platform, replyCtx any) {
	if finisher, ok := platform.(ReceiptFinisher); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		finisher.FinishReceipt(ctx, replyCtx)
	}
}

func (e *Engine) finishDroppedMessageReceipts(state *interactiveState) {
	state.mu.Lock()
	remaining := state.pendingMessages
	state.pendingMessages = nil
	state.mu.Unlock()
	for _, q := range remaining {
		e.finishMessageReceipt(q.platform, q.replyCtx)
	}
}
