package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

type reconnectHTTPClient func(*http.Request) (*http.Response, error)

func (f reconnectHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Exercise the production SDK, rather than duplicating its backoff formula.
// Virtual time makes a prolonged outage deterministic and fast.
func TestSocketReconnect_ProlongedOutageCapsBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts []time.Time
		client := slackapi.New("test-token", slackapi.OptionAppLevelToken("test-app-token"),
			slackapi.OptionHTTPClient(reconnectHTTPClient(func(*http.Request) (*http.Response, error) {
				attempts = append(attempts, time.Now())
				n := len(attempts)
				if n == 100 || (n > 1 && attempts[n-1].Sub(attempts[n-2]) > 5*time.Minute) {
					cancel()
				}
				return nil, errors.New("simulated network outage")
			})))
		socket := socketmode.New(client)
		if err := runSocketMode(ctx, socket.RunContext, socket.Events, func(socketmode.Event) {}); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown during backoff: got %v, want context cancellation", err)
		}
		hitCap := false
		for i := 1; i < len(attempts); i++ {
			delay := attempts[i].Sub(attempts[i-1])
			if delay <= 0 || delay > 5*time.Minute {
				t.Fatalf("attempt %d: retry delay %s is outside (0, 5m]", i+1, delay)
			}
			hitCap = hitCap || delay == 5*time.Minute
		}
		if len(attempts) != 100 || !hitCap {
			t.Fatalf("expected continued retries at the cap, got %d attempts, hitCap=%t", len(attempts), hitCap)
		}
	})
}

func TestSocketReconnect_PreservesServerRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts []time.Time
		client := slackapi.New("test-token", slackapi.OptionAppLevelToken("test-app-token"),
			slackapi.OptionHTTPClient(reconnectHTTPClient(func(*http.Request) (*http.Response, error) {
				attempts = append(attempts, time.Now())
				if len(attempts) == 1 {
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Header:     http.Header{"Retry-After": []string{"600"}},
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}
				cancel()
				return nil, errors.New("simulated network outage")
			})))
		socket := socketmode.New(client)
		if err := runSocketMode(ctx, socket.RunContext, socket.Events, func(socketmode.Event) {}); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown during backoff: %v", err)
		}
		if len(attempts) != 2 {
			t.Fatalf("got %d attempts, want 2", len(attempts))
		}
		if delay := attempts[1].Sub(attempts[0]); delay != 10*time.Minute {
			t.Fatalf("retry delay = %s, want server Retry-After of 10m", delay)
		}
	})
}

func TestSocketReconnect_CancelInterruptsCappedWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := slackapi.New("test-token", slackapi.OptionAppLevelToken("test-app-token"),
			slackapi.OptionHTTPClient(reconnectHTTPClient(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("simulated network outage")
			})))
		socket := socketmode.New(client)
		var canceledAt time.Time
		err := runSocketMode(ctx, socket.RunContext, socket.Events, func(evt socketmode.Event) {
			failure, ok := evt.Data.(*slackapi.ConnectionErrorEvent)
			if ok && failure.Backoff > 5*time.Minute {
				go func() {
					time.Sleep(time.Minute)
					canceledAt = time.Now()
					cancel()
				}()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown during capped wait: %v", err)
		}
		if canceledAt.IsZero() || !time.Now().Equal(canceledAt) {
			t.Fatal("shutdown waited for the reconnect timer instead of canceling immediately")
		}
	})
}

func TestSocketReconnect_ConnectionResetsCappedRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan socketmode.Event)
		runs, connections := 0, 0
		var shortRetryAt time.Time
		run := func(ctx context.Context) error {
			runs++
			if runs == 1 {
				events <- socketmode.Event{Type: socketmode.EventTypeConnectionError,
					Data: &slackapi.ConnectionErrorEvent{Backoff: time.Hour, ErrorObj: errors.New("network down")}}
				<-ctx.Done()
				return ctx.Err()
			}
			events <- socketmode.Event{Type: socketmode.EventTypeConnected}
			shortRetryAt = time.Now()
			events <- socketmode.Event{Type: socketmode.EventTypeConnectionError,
				Data: &slackapi.ConnectionErrorEvent{Backoff: time.Second, ErrorObj: errors.New("brief outage")}}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			events <- socketmode.Event{Type: socketmode.EventTypeConnected}
			<-ctx.Done()
			return ctx.Err()
		}
		err := runSocketMode(ctx, run, events, func(evt socketmode.Event) {
			if evt.Type == socketmode.EventTypeConnected {
				connections++
				if connections == 2 {
					cancel()
				}
			}
		})
		if !errors.Is(err, context.Canceled) || runs != 2 || connections != 2 {
			t.Fatalf("recovery: err=%v runs=%d connections=%d", err, runs, connections)
		}
		if delay := time.Since(shortRetryAt); delay != time.Second {
			t.Fatalf("retry after a successful connection took %s, want 1s", delay)
		}
	})
}
