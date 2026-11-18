//go:build unix

package interp

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func (r *Runner) bashPPStreamGroupLifecycle(ctx context.Context, group int) (func(), error) {
	if group <= 0 {
		return nil, fmt.Errorf("foreign stream: worker did not establish a process group")
	}
	var tty *foregroundJobTTY
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil && r.monitorActive() {
		// As with ordinary foreground commands, noninteractive monitor mode
		// need not have a controlling terminal. When it does, hand it over before
		// starting any pipeline child, so no child can race a terminal read.
		if candidate, err := runnerForegroundJobTTY(r); err == nil {
			if err = candidate.giveTo(group); err != nil {
				return nil, fmt.Errorf("foreign stream terminal handoff: %w", err)
			}
			tty = candidate
		}
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		_ = syscall.Kill(-group, syscall.SIGINT)
	})
	// In unmonitored mode the terminal still targets the shell's group.
	// Relay foreground INT/QUIT to the actual worker group, never to another
	// job or to the shell itself. Other runner signal/trap handling is retained.
	var stopSignals func()
	if bg == nil && tty == nil {
		ch := make(chan os.Signal, 4)
		done := make(chan struct{})
		exited := make(chan struct{})
		signal.Notify(ch, syscall.SIGINT, syscall.SIGQUIT)
		go func() {
			defer close(exited)
			for {
				select {
				case sig := <-ch:
					if sig, ok := sig.(syscall.Signal); ok {
						_ = syscall.Kill(-group, sig)
					}
				case <-done:
					return
				}
			}
		}()
		stopSignals = func() { signal.Stop(ch); close(done); <-exited }
	}
	return func() {
		if stopSignals != nil {
			stopSignals()
		}
		if !stopCancel() {
			<-cancelDone
		}
		if tty != nil {
			_ = tty.restore()
		}
	}, nil
}
