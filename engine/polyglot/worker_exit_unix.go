//go:build unix

package polyglot

import (
	"os"
	"syscall"
)

// workerDeath classifies how a worker ended. kill sends SIGKILL, so a SIGKILL
// death is kill's own doing (or indistinguishable from it) and not reported;
// any other signal, or a plain exit, happened to the worker on its own.
func workerDeath(state *os.ProcessState) workerExit {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return workerExit{}
	}
	if status.Signaled() {
		if status.Signal() == syscall.SIGKILL {
			return workerExit{}
		}
		return workerExit{died: true, signal: int(status.Signal()), code: -1}
	}
	if status.Exited() {
		return workerExit{died: true, code: status.ExitStatus()}
	}
	return workerExit{}
}

// naturalWorkerDeath classifies a process after its protocol pipe reached EOF
// and the caller reaped it without first sending a signal. Unlike workerDeath,
// every signal is foreign here, including SIGKILL.
func naturalWorkerDeath(state *os.ProcessState) workerExit {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return workerExit{}
	}
	if status.Signaled() {
		return workerExit{died: true, signal: int(status.Signal()), code: -1}
	}
	if status.Exited() {
		return workerExit{died: true, code: status.ExitStatus()}
	}
	return workerExit{}
}
