// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix && !linux && !darwin

package interp

import "golang.org/x/sys/unix"

// killSignals on the BSDs/Darwin is the portable signal set (no Linux-specific
// SIGSTKFLT/SIGPWR and no realtime signals), in bash's `kill -l` listing order.
var killSignals = []struct {
	Name string
	Sig  killSig
}{
	{"HUP", unix.SIGHUP},
	{"INT", unix.SIGINT},
	{"QUIT", unix.SIGQUIT},
	{"ILL", unix.SIGILL},
	{"TRAP", unix.SIGTRAP},
	{"ABRT", unix.SIGABRT},
	{"BUS", unix.SIGBUS},
	{"FPE", unix.SIGFPE},
	{"KILL", unix.SIGKILL},
	{"USR1", unix.SIGUSR1},
	{"SEGV", unix.SIGSEGV},
	{"USR2", unix.SIGUSR2},
	{"PIPE", unix.SIGPIPE},
	{"ALRM", unix.SIGALRM},
	{"TERM", unix.SIGTERM},
	{"CHLD", unix.SIGCHLD},
	{"CONT", unix.SIGCONT},
	{"STOP", unix.SIGSTOP},
	{"TSTP", unix.SIGTSTP},
	{"TTIN", unix.SIGTTIN},
	{"TTOU", unix.SIGTTOU},
	{"URG", unix.SIGURG},
	{"XCPU", unix.SIGXCPU},
	{"XFSZ", unix.SIGXFSZ},
	{"VTALRM", unix.SIGVTALRM},
	{"PROF", unix.SIGPROF},
	{"WINCH", unix.SIGWINCH},
	{"IO", unix.SIGIO},
	{"SYS", unix.SIGSYS},
}
