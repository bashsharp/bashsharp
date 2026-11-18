// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"fmt"
	"strings"
)

// rtSignalName reproduces bash's realtime-signal naming: RTMIN / RTMIN+n in the
// lower half of [rtmin,rtmax], RTMAX-n / RTMAX in the upper half
// (lib/sh/strsignal.c). Shared by the Linux table (SIGRTMIN=34) and the
// Cygwin/MSYS-numbered Windows table (SIGRTMIN=32).
func rtSignalName(i, rtmin, rtmax int) string {
	switch {
	case i == rtmin:
		return "RTMIN"
	case i == rtmax:
		return "RTMAX"
	case i-rtmin <= (rtmax-rtmin)/2:
		return fmt.Sprintf("RTMIN+%d", i-rtmin)
	default:
		return fmt.Sprintf("RTMAX-%d", rtmax-i)
	}
}

// signalDescriptions maps a canonical signal name (no "SIG" prefix, as used
// by killSignals) to bash's human-readable description (j_strsignal /
// siglist.c), e.g. "Segmentation fault". Note that bash 5.3 reports SIGFPE as
// "Arithmetic exception" (not "Floating point exception"), which is what we
// reproduce here.
var signalDescriptions = map[string]string{
	"HUP":    "Hangup",
	"INT":    "Interrupt",
	"QUIT":   "Quit",
	"ILL":    "Illegal instruction",
	"TRAP":   "Trace/breakpoint trap",
	"ABRT":   "Aborted",
	"BUS":    "Bus error",
	"FPE":    "Arithmetic exception",
	"KILL":   "Killed",
	"USR1":   "User defined signal 1",
	"SEGV":   "Segmentation fault",
	"USR2":   "User defined signal 2",
	"PIPE":   "Broken pipe",
	"ALRM":   "Alarm clock",
	"TERM":   "Terminated",
	"CHLD":   "Child exited",
	"CONT":   "Continued",
	"STOP":   "Stopped (signal)",
	"TSTP":   "Stopped",
	"TTIN":   "Stopped (tty input)",
	"TTOU":   "Stopped (tty output)",
	"URG":    "Urgent I/O condition",
	"XCPU":   "CPU time limit exceeded",
	"XFSZ":   "File size limit exceeded",
	"VTALRM": "Virtual timer expired",
	"PROF":   "Profiling timer expired",
	"WINCH":  "Window changed",
	"IO":     "I/O possible",
	"SYS":    "Bad system call",
}

// signalDescription returns bash's human-readable description for sig, e.g.
// SIGSEGV -> "Segmentation fault". ok is false for signals with no known
// description (in which case no death notification is printed).
func signalDescription(sig killSig) (desc string, ok bool) {
	name, ok := signalName(sig)
	if !ok {
		return "", false
	}
	desc, ok = signalDescriptions[name]
	return desc, ok
}

// signalListEntry is one row of `kill -l` / `trap -l`, in platform-neutral
// form so the layout can be rendered (and tested) for any signal table.
type signalListEntry struct {
	Num  int
	Name string
}

// formatSignalList renders bash's signal listing: POSIX mode prints the bare
// names space-separated on one line; default mode prints "NN) SIGNAME" cells,
// five per row, numbered by each signal's platform value.
func formatSignalList(entries []signalListEntry, posix bool) string {
	var sb strings.Builder
	if posix {
		for i, e := range entries {
			if i > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(e.Name)
		}
		sb.WriteString("\n")
		return sb.String()
	}
	col := 0
	for _, e := range entries {
		col++
		fmt.Fprintf(&sb, "%2d) SIG%-10s", e.Num, e.Name)
		if col%5 == 0 {
			sb.WriteString("\n")
		}
	}
	if col%5 != 0 {
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatSignalListTabs is the GNU Bash Unix layout. Unlike the Windows
// signal listing, cells are separated by tabs with no trailing padding.
func formatSignalListTabs(entries []signalListEntry) string {
	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			if i%5 == 0 {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte('\t')
			}
		}
		fmt.Fprintf(&sb, "%2d) SIG%s", e.Num, e.Name)
	}
	if len(entries) > 0 {
		sb.WriteByte('\n')
	}
	return sb.String()
}
