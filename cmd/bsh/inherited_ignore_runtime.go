//go:build bashy_runtime_snapshot && (linux || darwin)

package main

import (
	"strings"
	"syscall"
	_ "unsafe" // go:linkname
)

// Provided by the product runtime overlay (tools/productgo), which captures
// inherited SIG_IGN dispositions before the Go runtime installs its handlers.
//
//go:linkname bashyInheritedIgnoredSignals runtime.bashyInheritedIgnoredSignals
func bashyInheritedIgnoredSignals() uint64

var inheritedSignalNames = []struct {
	sig  syscall.Signal
	name string
}{
	{syscall.SIGHUP, "HUP"}, {syscall.SIGINT, "INT"}, {syscall.SIGQUIT, "QUIT"},
	{syscall.SIGILL, "ILL"}, {syscall.SIGABRT, "ABRT"}, {syscall.SIGFPE, "FPE"},
	{syscall.SIGSEGV, "SEGV"}, {syscall.SIGPIPE, "PIPE"}, {syscall.SIGALRM, "ALRM"},
	{syscall.SIGTERM, "TERM"}, {syscall.SIGBUS, "BUS"}, {syscall.SIGUSR1, "USR1"},
	{syscall.SIGUSR2, "USR2"}, {syscall.SIGTSTP, "TSTP"}, {syscall.SIGTTIN, "TTIN"},
	{syscall.SIGTTOU, "TTOU"}, {syscall.SIGCONT, "CONT"}, {syscall.SIGXCPU, "XCPU"},
	{syscall.SIGXFSZ, "XFSZ"},
}

// Product builds read the runtime's pre-handler snapshot in pure Go, so no
// platform needs cgo for the shell's inherited-ignore contract.
func preGoIgnoredSignals() string {
	mask := bashyInheritedIgnoredSignals()
	var names []string
	for _, s := range inheritedSignalNames {
		if mask&(1<<uint(s.sig)) != 0 {
			names = append(names, s.name)
		}
	}
	return strings.Join(names, ",")
}
