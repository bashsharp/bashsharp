// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build linux

package interp

import "golang.org/x/sys/unix"

// killSignals on Linux is the full signal set bash lists for `kill -l`: the
// standard glibc userland convention (SIGRTMIN=34, SIGRTMAX=64), including the
// Linux-specific SIGSTKFLT/SIGPWR and the realtime signals SIGRTMIN..SIGRTMAX.
// bashy is a static pure-Go binary (no libc) so it follows glibc's conventional
// numbering, which is what real Linux distributions present.
var killSignals = linuxKillSignals()

func linuxKillSignals() []struct {
	Name string
	Sig  killSig
} {
	sigs := []struct {
		Name string
		Sig  killSig
	}{
		{"HUP", unix.SIGHUP}, {"INT", unix.SIGINT}, {"QUIT", unix.SIGQUIT}, {"ILL", unix.SIGILL},
		{"TRAP", unix.SIGTRAP}, {"ABRT", unix.SIGABRT}, {"BUS", unix.SIGBUS}, {"FPE", unix.SIGFPE},
		{"KILL", unix.SIGKILL}, {"USR1", unix.SIGUSR1}, {"SEGV", unix.SIGSEGV}, {"USR2", unix.SIGUSR2},
		{"PIPE", unix.SIGPIPE}, {"ALRM", unix.SIGALRM}, {"TERM", unix.SIGTERM}, {"STKFLT", unix.SIGSTKFLT},
		{"CHLD", unix.SIGCHLD}, {"CONT", unix.SIGCONT}, {"STOP", unix.SIGSTOP}, {"TSTP", unix.SIGTSTP},
		{"TTIN", unix.SIGTTIN}, {"TTOU", unix.SIGTTOU}, {"URG", unix.SIGURG}, {"XCPU", unix.SIGXCPU},
		{"XFSZ", unix.SIGXFSZ}, {"VTALRM", unix.SIGVTALRM}, {"PROF", unix.SIGPROF}, {"WINCH", unix.SIGWINCH},
		{"IO", unix.SIGIO}, {"PWR", unix.SIGPWR}, {"SYS", unix.SIGSYS},
	}
	// Real-time signals SIGRTMIN..SIGRTMAX. bash names them RTMIN, RTMIN+1 … up
	// to the midpoint, then RTMAX-n … RTMAX (lib/sh/strsignal.c).
	const rtmin, rtmax = 34, 64
	for i := rtmin; i <= rtmax; i++ {
		sigs = append(sigs, struct {
			Name string
			Sig  killSig
		}{rtSignalName(i, rtmin, rtmax), killSig(i)})
	}
	return sigs
}
