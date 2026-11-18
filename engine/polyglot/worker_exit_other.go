//go:build !unix

package polyglot

import "os"

// workerDeath preserves native exit codes. Windows Process.Kill uses
// TerminateProcess with status 1; that status is indistinguishable from our
// cleanup and is excluded, like SIGKILL in the Unix implementation.
func workerDeath(state *os.ProcessState) workerExit {
	code := state.ExitCode()
	if code < 0 || code == 1 {
		return workerExit{}
	}
	return workerExit{died: true, code: code}
}

// naturalWorkerDeath classifies a process reaped after protocol EOF, without
// first calling Process.Kill. Status 1 is therefore unambiguous on Windows.
func naturalWorkerDeath(state *os.ProcessState) workerExit {
	code := state.ExitCode()
	if code < 0 {
		return workerExit{}
	}
	return workerExit{died: true, code: code}
}
