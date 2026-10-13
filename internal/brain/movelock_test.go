package brain

import (
	"context"
	"testing"
	"time"
)

func TestMoveLockDriverHoldBeatsABrainStep(t *testing.T) {
	l := NewMoveLock()
	ctx, done, ok := l.begin(context.Background())
	if !ok {
		t.Fatal("a free lock refused a step")
	}
	if _, _, ok2 := l.begin(context.Background()); ok2 {
		t.Fatal("two steps at once")
	}
	held := make(chan func())
	go func() {
		rel, err := l.Hold(context.Background())
		if err != nil {
			t.Error(err)
		}
		held <- rel
	}()
	select {
	case <-ctx.Done(): // the driver cancels the step
	case <-time.After(2 * time.Second):
		t.Fatal("the step was not cancelled")
	}
	select {
	case <-held:
		t.Fatal("Hold returned before the step ended")
	case <-time.After(50 * time.Millisecond):
	}
	done()
	var rel func()
	select {
	case rel = <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("Hold never returned")
	}
	if !l.Held() {
		t.Fatal("not held")
	}
	if _, _, ok := l.begin(context.Background()); ok {
		t.Fatal("a step began under the driver's hold")
	}
	rel()
	rel() // twice is safe
	if l.Held() {
		t.Fatal("still held")
	}
	if _, d, ok := l.begin(context.Background()); !ok {
		t.Fatal("no step after release")
	} else {
		d()
	}
}

func TestMoveLockHoldsNestAndAbortWithTheContext(t *testing.T) {
	l := NewMoveLock()
	r1, _ := l.Hold(context.Background())
	r2, _ := l.Hold(context.Background())
	r1()
	if !l.Held() {
		t.Fatal("the second hold was lost")
	}
	r2()
	if l.Held() {
		t.Fatal("held")
	}
	_, done, _ := l.begin(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The step never ends, but the cancelled context frees the driver's wait (the step is cancelled as well).
	if _, err := l.Hold(ctx); err == nil {
		t.Fatal("Hold waited for a step that was still running and returned no error")
	}
	if l.Held() {
		t.Fatal("a failed Hold kept the lock")
	}
	done()
	var nilLock *MoveLock
	if nilLock.Held() {
		t.Fatal("nil lock held")
	}
	rel, err := nilLock.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if _, d, ok := nilLock.begin(context.Background()); !ok {
		t.Fatal("nil lock refused a step")
	} else {
		d()
	}
}
