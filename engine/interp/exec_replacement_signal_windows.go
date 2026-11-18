//go:build windows

package interp

import (
	"os"
	"os/signal"
	"syscall"
)

var bashPPKernel32 = syscall.NewLazyDLL("kernel32.dll")
var bashPPGenerateConsoleCtrlEvent = bashPPKernel32.NewProc("GenerateConsoleCtrlEvent")

func forwardExecReplacementSignals(pid int) func() { return func() {} }

func forwardExecReplacementSignalsWithReport(pid int, report func(int)) func() {
	return forwardExecReplacementSignals(pid)
}

// The helper is in its own console group. Catch the break at the interpreter
// long enough to deliver it there and finish bridge/scratch cleanup after the
// helper exits. os.Interrupt includes CTRL_BREAK_EVENT on Windows.
func forwardBashPPNativeSignalsWithReport(pid int, report func(int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		for {
			select {
			case <-ch:
				const ctrlBreakEvent = 1
				if sent, _, _ := bashPPGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(uint32(pid))); sent != 0 && report != nil {
					report(int(syscall.SIGINT))
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		<-exited
	}
}
