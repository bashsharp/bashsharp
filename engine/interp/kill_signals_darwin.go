// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build darwin

package interp

import "golang.org/x/sys/unix"

// killSignals matches Darwin's signal names and numbers as listed by Bash.
var killSignals = []struct {
	Name string
	Sig  killSig
}{
	{"HUP", unix.SIGHUP}, {"INT", unix.SIGINT}, {"QUIT", unix.SIGQUIT}, {"ILL", unix.SIGILL},
	{"TRAP", unix.SIGTRAP}, {"ABRT", unix.SIGABRT}, {"EMT", unix.SIGEMT}, {"FPE", unix.SIGFPE},
	{"KILL", unix.SIGKILL}, {"BUS", unix.SIGBUS}, {"SEGV", unix.SIGSEGV}, {"SYS", unix.SIGSYS},
	{"PIPE", unix.SIGPIPE}, {"ALRM", unix.SIGALRM}, {"TERM", unix.SIGTERM}, {"URG", unix.SIGURG},
	{"STOP", unix.SIGSTOP}, {"TSTP", unix.SIGTSTP}, {"CONT", unix.SIGCONT}, {"CHLD", unix.SIGCHLD},
	{"TTIN", unix.SIGTTIN}, {"TTOU", unix.SIGTTOU}, {"IO", unix.SIGIO}, {"XCPU", unix.SIGXCPU},
	{"XFSZ", unix.SIGXFSZ}, {"VTALRM", unix.SIGVTALRM}, {"PROF", unix.SIGPROF}, {"WINCH", unix.SIGWINCH},
	{"INFO", unix.SIGINFO}, {"USR1", unix.SIGUSR1}, {"USR2", unix.SIGUSR2},
}
