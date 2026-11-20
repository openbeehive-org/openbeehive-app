package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// goneOnSecondLook is a context that is cancelled the second time anyone
// asks for its Done channel: the subscribe loop looks first and waits, the
// poll query it starts next looks again and finds the client gone.
type goneOnSecondLook struct {
	context.Context
	mu    sync.Mutex
	looks int
	done  chan struct{}
}

func (c *goneOnSecondLook) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.looks++; c.looks == 2 {
		close(c.done)
	}
	return c.done
}

func (c *goneOnSecondLook) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// TestSubscribeEndsQuietlyWhenTheClientLeavesMidPoll: a device that
// disconnects while the poll query is running ends its stream like any other
// disconnect, not as an internal error.
func TestSubscribeEndsQuietlyWhenTheClientLeavesMidPoll(t *testing.T) {
	svc := newSyncFixture(t)
	svc.poll = time.Millisecond
	ctx := &goneOnSecondLook{Context: context.Background(), done: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- svc.subscribe(ctx, auth.Identity{UserID: "user-a", OrgID: "org-a"}, 0,
			func(*wv1.SubscribeEvent) error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subscribe ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe did not end")
	}
}
