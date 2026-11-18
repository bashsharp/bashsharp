//go:build full

package interp_test

// Sprint: #319; Story: #1083; Story-ID: 91b27c7c0b52

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	s319ReplayLog        = "BASHPP_S319_REPLAY_LOG"
	s319CycleLauncher    = "BASHPP_S319_CYCLE_LAUNCHER"
	s319ReplayLimitEnv   = "BASHPP_REEXEC_REPLAY_LIMIT"
	s319DepthLimitEnv    = "BASHPP_REEXEC_REPLAY_DEPTH_LIMIT"
	s319ReplacementTool  = "BASHPP_S319_REPLACEMENT_TOOL"
	s319ReplayHoldPeriod = 300 * time.Millisecond
)

// TestGoSourceS319ReexecBoundsConcurrentReplays measures the shape which
// exhausted the Sprint 319 diagnostic host. The Go command schedules a tool
// invocation as if it were a short-lived process and starts -p of them at once,
// but every invocation of a replacement launcher is an entire interpreter. The
// launcher must therefore admit replays against one bound shared by every copy
// of it, so the live replay count stays a property of the host rather than the
// product of each nesting level's -p.
func TestGoSourceS319ReexecBoundsConcurrentReplays(t *testing.T) {
	if args, ok := s319ReplayArgs(); ok && args[1] == "replay" {
		s319MarkReplay(t, "+")
		time.Sleep(s319ReplayHoldPeriod)
		s319MarkReplay(t, "-")
		return
	}
	const replays = 8
	for _, tc := range []struct {
		name  string
		limit string
		// peak is the largest number of replays which may be live at once. An
		// unadmitted launcher is bounded only by how many the caller starts.
		peak int
	}{
		{name: "unadmitted", limit: "0", peak: replays},
		{name: "bounded", limit: "2", peak: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(s319ReplayLimitEnv, tc.limit)
			launcher, log := s319Launcher(t, "TestGoSourceS319ReexecBoundsConcurrentReplays")
			s319RunConcurrently(t, launcher, replays, "replay")
			live, done := s319ReplayPeak(t, log)
			if done != replays {
				t.Fatalf("completed replays = %d, want %d", done, replays)
			}
			if live > tc.peak {
				t.Errorf("peak live replays = %d, want at most %d", live, tc.peak)
			}
			if tc.limit == "0" && live < 3 {
				// Without the bound the launcher must show the unadmitted
				// shape; a bound which happened to be enforced elsewhere would
				// make the bounded case above prove nothing.
				t.Errorf("peak live replays = %d without a bound, want more than 2", live)
			}
		})
	}
}

// TestGoSourceS319ReexecRefusesReplayCycle replays a program which reaches its
// own launcher again, the cycle a replaced Go tool forms when the replay's own
// package discovery invokes the replacement. The launcher must refuse the cycle
// at a bounded depth with a diagnostic naming the plan, rather than multiplying
// interpreters until the host is exhausted.
func TestGoSourceS319ReexecRefusesReplayCycle(t *testing.T) {
	if args, ok := s319ReplayArgs(); ok && args[1] == "cycle" {
		s319MarkReplay(t, "+")
		nested := exec.Command(os.Getenv(s319CycleLauncher), "cycle")
		// TestMain republishes GOSH_PROG in every test process, which would
		// make the next replay of this binary run as a shell instead.
		nested.Env = s281WithoutEnv(os.Environ(), "GOSH_PROG")
		output, _ := nested.CombinedOutput()
		// Chain the nested launcher's report outward so the outermost caller
		// sees why the cycle ended.
		_, _ = os.Stderr.Write(output)
		s319MarkReplay(t, "-")
		return
	}
	for _, tc := range []struct {
		name string
		// limit is the configured depth limit, empty for the default. replays
		// must equal it: the outermost replay runs at depth 0, so the limit is
		// reached by the launcher the last admitted replay invokes. Nothing
		// else ends the cycle, which is why the count tracks the limit.
		limit   string
		replays int
	}{
		{name: "configured limit", limit: "2", replays: 2},
		{name: "default limit", replays: 4}, // reexecReplayDepthLimit
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(s319DepthLimitEnv, tc.limit)
			launcher, log := s319Launcher(t, "TestGoSourceS319ReexecRefusesReplayCycle")
			command := exec.Command(launcher, "cycle")
			command.Env = append(s281WithoutEnv(os.Environ(), "GOSH_PROG"), s319CycleLauncher+"="+launcher)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("replay cycle: %v: %s", err, output)
			}
			if _, done := s319ReplayPeak(t, log); done != tc.replays {
				t.Errorf("replays before the cycle was refused = %d, want %d; output=%s", done, tc.replays, output)
			}
			want := fmt.Sprintf("replay nesting reached depth %d of %d", tc.replays, tc.replays)
			if !strings.Contains(string(output), want) {
				t.Errorf("replay cycle output = %s, want it to contain %q", output, want)
			}
		})
	}
}

// TestGoSourceS319ReexecReplayIdentityIsContentAddressed pins the launcher's Go
// tool identity to the replayed program's own content. A tool identity keys the
// build cache: an identity derived from the session's launcher workspace gives
// the same program a new one in every session, so every session recompiles
// every package the replayed tool produces.
func TestGoSourceS319ReexecReplayIdentityIsContentAddressed(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	companion := filepath.Join(dir, "companion.go")
	if err := os.WriteFile(companion, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := []string{self, "-test.run=^$", "--"}
	withCompanion := []string{self, "-test.run=^$", companion, "--"}

	first := s319ToolIdentity(t, dir, "first", plan)
	second := s319ToolIdentity(t, dir, "second", plan)
	if first != second {
		t.Errorf("tool identity differs between sessions replaying one plan:\n%s\n%s", first, second)
	}
	named := s319ToolIdentity(t, dir, "named", withCompanion)
	if named == first {
		t.Errorf("tool identity %s ignores a plan argument", named)
	}
	if err := os.WriteFile(companion, []byte("package main\nvar changed = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := s319ToolIdentity(t, dir, "changed", withCompanion)
	if changed == named {
		t.Errorf("tool identity %s ignores the contents of a plan argument", changed)
	}
}

// s319ToolIdentity builds a launcher for plan in its own interpreter session,
// then reports what it answers a Go tool identity probe with.
func s319ToolIdentity(t *testing.T, dir, name string, plan []string) string {
	t.Helper()
	// A Go tool's own name is part of its identity, so every launcher under
	// review must be installed under one name in a directory of its own.
	launcher := s319ToolPath(t, filepath.Join(dir, name))
	t.Setenv(s319ReplacementTool, launcher)
	runS281ReexecSourceProgram(t, s319ReexecVersionCycleSource, nil, nil, plan)
	command := exec.Command(launcher, "-V=full")
	command.Env = s281WithoutEnv(os.Environ(), "GOSH_PROG")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("version probe: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

// s319Launcher builds a launcher whose plan replays test, copies it out of the
// session's workspace under a Go tool's name, and reports it with the file the
// replays record themselves in.
func s319Launcher(t *testing.T, test string) (launcher, log string) {
	t.Helper()
	dir := t.TempDir()
	launcher = s319ToolPath(t, dir)
	log = filepath.Join(dir, "replays")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(s319ReplacementTool, launcher)
	t.Setenv(s319ReplayLog, log)
	runS281ReexecSourceProgram(t, s319ReexecVersionCycleSource, nil, nil,
		[]string{self, "-test.run=^" + test + "$", "--"})
	return launcher, log
}

// s319ToolPath reports the path a launcher copy takes the name of a Go tool
// under, creating its directory.
func s319ToolPath(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "compile"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

// s319ReplayArgs reports the arguments a replayed test binary was given after
// the plan's program-argument separator.
func s319ReplayArgs() ([]string, bool) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	return args, len(args) == 2
}

func s319RunConcurrently(t *testing.T, launcher string, count int, arg string) {
	t.Helper()
	env := s281WithoutEnv(os.Environ(), "GOSH_PROG")
	failures := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			command := exec.Command(launcher, arg)
			command.Env = env
			if output, err := command.CombinedOutput(); err != nil {
				failures <- fmt.Errorf("replay: %w: %s", err, output)
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

// s319MarkReplay records one edge of a replay's lifetime. A single-byte append
// is atomic, so concurrent replays interleave without losing a mark.
func s319MarkReplay(t *testing.T, mark string) {
	t.Helper()
	log := os.Getenv(s319ReplayLog)
	file, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(mark); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// s319ReplayPeak reports the most replays which were live at once and how many
// ran to completion.
func s319ReplayPeak(t *testing.T, log string) (peak, done int) {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, mark := range string(data) {
		switch mark {
		case '+':
			live++
			if live > peak {
				peak = live
			}
		case '-':
			live--
			done++
		default:
			t.Fatalf("replay log %q has an unexpected mark %q", data, mark)
		}
	}
	return peak, done
}
