// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"strings"
	"testing"
)

// Sprint 245, story S245.4: the Windows signal model is proven here on any
// host; signal_windows_test.go exercises the syscalls on a Windows worker.

func TestWindowsSignalTableRoundTrips(t *testing.T) {
	t.Parallel()
	table := windowsSignalTable()
	if len(table) != 64 {
		t.Fatalf("table has %d entries, want 64 (31 named + RTMIN..RTMAX)", len(table))
	}
	seenNum := map[int]bool{}
	seenName := map[string]bool{}
	for i, e := range table {
		if e.Num != i+1 {
			t.Errorf("entry %d (%s) has number %d; the table must be dense 1..64", i, e.Name, e.Num)
		}
		if seenNum[e.Num] || seenName[e.Name] {
			t.Errorf("duplicate entry %+v", e)
		}
		seenNum[e.Num], seenName[e.Name] = true, true
		for _, spelling := range []string{e.Name, "SIG" + e.Name, strings.ToLower(e.Name), "sig" + strings.ToLower(e.Name)} {
			got, ok := windowsSignalByName(spelling)
			if !ok || got != e {
				t.Errorf("windowsSignalByName(%q) = %+v, %v; want %+v", spelling, got, ok, e)
			}
		}
		got, ok := windowsSignalByNumber(e.Num)
		if !ok || got != e {
			t.Errorf("windowsSignalByNumber(%d) = %+v, %v; want %+v", e.Num, got, ok, e)
		}
	}
	// Cygwin/MSYS numbering, as the fixtures and $BASH_TRAPSIG expect.
	for name, num := range map[string]int{
		"HUP": 1, "INT": 2, "QUIT": 3, "ABRT": 6, "EMT": 7, "KILL": 9, "BUS": 10,
		"SEGV": 11, "SYS": 12, "PIPE": 13, "TERM": 15, "STOP": 17, "CONT": 19,
		"CHLD": 20, "WINCH": 28, "PWR": 29, "USR1": 30, "USR2": 31,
		"RTMIN": 32, "RTMIN+1": 33, "RTMIN+16": 48, "RTMAX-15": 49, "RTMAX-1": 63, "RTMAX": 64,
	} {
		if e, ok := windowsSignalByName(name); !ok || e.Num != num {
			t.Errorf("%s = %d (%v), want %d", name, e.Num, ok, num)
		}
	}
	for _, bad := range []string{"", "SIG", "STKFLT", "INFO", "EXIT", "RTMIN+33", "65", "0"} {
		if e, ok := windowsSignalByName(bad); ok {
			t.Errorf("windowsSignalByName(%q) = %+v, want miss", bad, e)
		}
	}
	for _, bad := range []int{0, -1, 65, 128, 143} {
		if e, ok := windowsSignalByNumber(bad); ok {
			t.Errorf("windowsSignalByNumber(%d) = %+v, want miss", bad, e)
		}
	}
}

func TestWindowsSignalSets(t *testing.T) {
	t.Parallel()
	for _, e := range windowsSignalTable() {
		stops := e.Name == "STOP" || e.Name == "TSTP" || e.Name == "TTIN" || e.Name == "TTOU"
		conts := e.Name == "CONT"
		noDeath := stops || conts || e.Name == "CHLD" || e.Name == "URG" || e.Name == "WINCH"
		silent := e.Name == "INT" || e.Name == "PIPE"
		if got := windowsSignalStopsJob(e.Num); got != stops {
			t.Errorf("%s stops=%v want %v", e.Name, got, stops)
		}
		if got := windowsSignalContinuesJob(e.Num); got != conts {
			t.Errorf("%s continues=%v want %v", e.Name, got, conts)
		}
		if got := windowsSignalDefaultDoesNotTerminate(e.Num); got != noDeath {
			t.Errorf("%s defaultDoesNotTerminate=%v want %v", e.Name, got, noDeath)
		}
		if got := windowsSignalDeathSilent(e.Num); got != silent {
			t.Errorf("%s deathSilent=%v want %v", e.Name, got, silent)
		}
	}
}

func TestWindowsSignalListLayout(t *testing.T) {
	t.Parallel()
	got := formatSignalList(windowsSignalListEntries(), false)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 13 {
		t.Fatalf("kill -l printed %d lines, want 13 (64 signals, 5 per row):\n%s", len(lines), got)
	}
	// Same cell format the Unix listing is pinned to (interp_test.go).
	if want := " 1) SIGHUP        2) SIGINT        3) SIGQUIT       4) SIGILL        5) SIGTRAP      "; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	if want := "26) SIGVTALRM    27) SIGPROF      28) SIGWINCH     29) SIGPWR       30) SIGUSR1      "; lines[5] != want {
		t.Errorf("sixth line %q, want %q", lines[5], want)
	}
	if want := "31) SIGUSR2      32) SIGRTMIN     33) SIGRTMIN+1   34) SIGRTMIN+2   35) SIGRTMIN+3   "; lines[6] != want {
		t.Errorf("seventh line %q, want %q", lines[6], want)
	}
	if want := "61) SIGRTMAX-3   62) SIGRTMAX-2   63) SIGRTMAX-1   64) SIGRTMAX     "; lines[12] != want {
		t.Errorf("last line %q, want %q", lines[12], want)
	}
	posix := formatSignalList(windowsSignalListEntries(), true)
	if !strings.HasPrefix(posix, "HUP INT QUIT ILL TRAP ABRT EMT FPE KILL BUS SEGV SYS PIPE ALRM TERM ") ||
		!strings.HasSuffix(posix, " USR1 USR2 RTMIN RTMIN+1 RTMIN+2 RTMIN+3 RTMIN+4 RTMIN+5 RTMIN+6 RTMIN+7 RTMIN+8 RTMIN+9 RTMIN+10 RTMIN+11 RTMIN+12 RTMIN+13 RTMIN+14 RTMIN+15 RTMIN+16 RTMAX-15 RTMAX-14 RTMAX-13 RTMAX-12 RTMAX-11 RTMAX-10 RTMAX-9 RTMAX-8 RTMAX-7 RTMAX-6 RTMAX-5 RTMAX-4 RTMAX-3 RTMAX-2 RTMAX-1 RTMAX\n") ||
		strings.Count(posix, "\n") != 1 {
		t.Errorf("posix listing = %q", posix)
	}
}

// The Unix listing is rendered by the same formatter from the platform
// table; the Darwin and Linux first lines are pinned by TestRunnerRun. This
// guards the formatter against a table that is not in listing order.
func TestFormatSignalListUsesEntryNumbers(t *testing.T) {
	t.Parallel()
	got := formatSignalList([]signalListEntry{{10, "USR1"}, {12, "USR2"}}, false)
	if want := "10) SIGUSR1      12) SIGUSR2      \n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := formatSignalList(nil, false); got != "" {
		t.Errorf("empty table printed %q", got)
	}
}

func TestSignalMarkerCodec(t *testing.T) {
	t.Parallel()
	for num := 1; num <= 64; num++ {
		code := encodeSignalMarker(num)
		if code>>16 != 0x7E5A || code&0xFF != uint32(num) {
			t.Errorf("encode(%d) = %#x", num, code)
		}
		if got, ok := decodeSignalMarker(code); !ok || got != num {
			t.Errorf("decode(%#x) = %d, %v; want %d", code, got, ok, num)
		}
		if SignalMarkerExitCode(num) != int(code) {
			t.Errorf("SignalMarkerExitCode(%d) = %d, want %d", num, SignalMarkerExitCode(num), code)
		}
		// The exported pair is what a host reaping a bashy-owned child
		// itself (a Windows job carrier) round-trips a signal death through.
		if got, ok := SignalFromMarkerExitCode(SignalMarkerExitCode(num)); !ok || got != num {
			t.Errorf("SignalFromMarkerExitCode(%d) = %d, %v; want %d", SignalMarkerExitCode(num), got, ok, num)
		}
	}
	for _, code := range []uint32{0, 1, 15, 128, 143, 137, 0x7E5A0000, 0x7E5A0041, 0x7E5A00FF, 0x7E5B000F, 0xFE5A000F, 0xFFFFFFFF} {
		if num, ok := decodeSignalMarker(code); ok {
			t.Errorf("decode(%#x) = %d, want no signal", code, num)
		}
		if num, ok := SignalFromMarkerExitCode(int(int32(code))); ok {
			t.Errorf("SignalFromMarkerExitCode(%#x) = %d, want no signal", code, num)
		}
	}
	// The marker keeps the sign bit clear so a 32-bit int exit code stays
	// positive on GOARCH=386.
	if encodeSignalMarker(64)&0x80000000 != 0 {
		t.Error("marker sets the sign bit")
	}
}

func TestDecodeWindowsWaitStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		recorded int
		exit     uint32
		wantSig  int
		wantOK   bool
	}{
		{recorded: 15, exit: 1, wantSig: 15, wantOK: true},                              // recorded pid beats a clobbered exit code
		{recorded: 15, exit: encodeSignalMarker(9), wantSig: 15, wantOK: true},          // recorded pid beats the marker
		{recorded: 0, exit: encodeSignalMarker(15), wantSig: 15, wantOK: true},          // marker from a bashy child
		{recorded: 0, exit: encodeSignalMarker(30), wantSig: 30, wantOK: true},          // USR1 default action
		{recorded: 0, exit: 143, wantSig: 0, wantOK: false},                             // plain `exit 143` is not SIGTERM
		{recorded: 0, exit: 137, wantSig: 0, wantOK: false},                             // plain `exit 137` is not SIGKILL
		{recorded: 0, exit: 0, wantSig: 0, wantOK: false},                               // normal exit
		{recorded: 0, exit: 1, wantSig: 0, wantOK: false},                               // Process.Kill's exit code
		{recorded: 0, exit: 0xC000013A, wantSig: 0, wantOK: false},                      // STATUS_CONTROL_C_EXIT is not a marker
		{recorded: 0, exit: uint32(signalMarkerBase | 0x80), wantSig: 0, wantOK: false}, // out-of-table number
	}
	for _, c := range cases {
		got, ok := decodeWindowsWaitStatus(c.recorded, c.exit)
		if ok != c.wantOK || got.Signaled() != c.wantOK || got.Signal() != c.wantSig || got.CoreDump() {
			t.Errorf("decode(recorded=%d, exit=%#x) = {sig %d} ok=%v; want sig %d ok=%v", c.recorded, c.exit, got.Signal(), ok, c.wantSig, c.wantOK)
		}
	}
}

func TestWindowsSignalDeathNotice(t *testing.T) {
	t.Parallel()
	if got, ok := windowsSignalDeathNotice(15, []string{"sleep", "10"}); !ok || got != "Terminated sleep 10" {
		t.Errorf("TERM notice = %q, %v", got, ok)
	}
	if got, ok := windowsSignalDeathNotice(9, []string{"cat"}); !ok || got != "Killed cat" {
		t.Errorf("KILL notice = %q, %v", got, ok)
	}
	if got, ok := windowsSignalDeathNotice(30, []string{"x"}); !ok || got != "User defined signal 1 x" {
		t.Errorf("USR1 notice = %q, %v", got, ok)
	}
	for _, silent := range []int{2, 13, 0, 65, 32} {
		if got, ok := windowsSignalDeathNotice(silent, []string{"x"}); ok {
			t.Errorf("signal %d notice = %q, want none", silent, got)
		}
	}
}

func TestSignalPipeWireFormat(t *testing.T) {
	t.Parallel()
	if got := signalPipeName(4242); got != `\\.\pipe\bashy-sig-4242` {
		t.Errorf("pipe name %q", got)
	}
	if got := string(encodeSignalMessage(30)); got != "30\n" {
		t.Errorf("message %q", got)
	}
	for _, c := range []struct {
		msg  string
		num  int
		want bool
	}{
		{"30\n", 30, true}, {"15", 15, true}, {" 9 \r\n", 9, true}, {"64\n", 64, true},
		{"", 0, false}, {"\n", 0, false}, {"0\n", 0, false}, {"65\n", 0, false},
		{"-1\n", 0, false}, {"USR1\n", 0, false}, {"3x\n", 0, false},
	} {
		num, ok := decodeSignalMessage([]byte(c.msg))
		if ok != c.want || num != c.num {
			t.Errorf("decode(%q) = %d, %v; want %d, %v", c.msg, num, ok, c.num, c.want)
		}
	}
}

// BASHY_HARD_IGNORE carries the ignored set into a child bashy by canonical
// name. Under the Windows table USR2 exists, so the bridge can record it.
func TestWindowsHardIgnoreNamesResolve(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"USR2", "SIGUSR2", "usr1", "HUP", "TERM", "WINCH"} {
		if _, ok := windowsSignalByName(name); !ok {
			t.Errorf("hard-ignore name %q is not in the Windows table", name)
		}
	}
}
