package interp

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestGoSourceLocalTimeSleep(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	req := bashPPEvalRequest{Imports: map[string]string{"clock": "time"}}
	call := func(text string) (bool, error) {
		return r.goSourceLocalTimeSleep(context.Background(), req, bashPPBridgeRequest{
			Op:       "call",
			Selector: "clock.Sleep",
			Args:     []bashPPBridgeValue{{Kind: "int", Type: "time.Duration", Text: text}},
		})
	}

	if handled, err := call("0"); !handled || err != nil {
		t.Fatalf("Sleep(0): handled=%v err=%v", handled, err)
	}
	start := time.Now()
	if handled, err := call(strconv.FormatInt(int64(2*time.Millisecond), 10)); !handled || err != nil {
		t.Fatalf("Sleep(2ms): handled=%v err=%v", handled, err)
	}
	if elapsed := time.Since(start); elapsed < time.Millisecond {
		t.Fatalf("Sleep(2ms) returned after %s", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handled, err := r.goSourceLocalTimeSleep(ctx, req, bashPPBridgeRequest{
		Op:       "call",
		Selector: "clock.Sleep",
		Args:     []bashPPBridgeValue{{Kind: "int", Type: "time.Duration", Text: "1000000000"}},
	})
	if !handled || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Sleep: handled=%v err=%v", handled, err)
	}

	r.bashPPGoTask = true
	if handled, err := call("0"); handled || err != nil {
		t.Fatalf("task Sleep must retain bridge path: handled=%v err=%v", handled, err)
	}
}
