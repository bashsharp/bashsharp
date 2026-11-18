//go:build unix

package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// The owner deliberately does not perform a parent-side terminal handoff after
// Start. The child must already own its terminal on entry, independent of which
// process the scheduler runs first. Each subprocess has its own controlling PTY.
func TestForegroundCommandStartsWithTerminal(t *testing.T) {
	const modeKey = "SH_FOREGROUND_START_TEST"
	mode := os.Getenv(modeKey)
	if mode == "probe" {
		fd, err := unix.Open("/dev/tty", unix.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			t.Fatal(err)
		}
		if foreground != syscall.Getpgrp() {
			t.Fatalf("child started before handoff: foreground=%d child=%d", foreground, syscall.Getpgrp())
		}
		return
	}
	if mode != "" {
		redirected := strings.HasSuffix(mode, "/redirected")
		stdin := os.Stdin
		if redirected {
			var err error
			stdin, err = os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
		}
		r, err := New(StdIO(stdin, os.Stdout, os.Stderr), Params("-m"))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Reset()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if strings.HasPrefix(mode, "start/") {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundCommandStartsWithTerminal$")
			cmd.Env = foregroundStartEnv(modeKey, "probe")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, os.Stdout, os.Stderr
			tty := prepareForegroundJobCmd(ctx, r, cmd)
			if tty == nil {
				t.Fatal("foreground command was not prepared")
			}
			defer func() {
				if tty != nil {
					_ = tty.restore()
				}
			}()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := tty.restore(); err != nil {
				t.Fatal(err)
			}
			if redirected {
				if _, err := unix.FcntlInt(uintptr(tty.fd), unix.F_GETFD, 0); !errors.Is(err, syscall.EBADF) {
					t.Fatalf("owned tty descriptor not closed: %v", err)
				}
			}
			tty = nil
		} else {
			script := filepath.Join(t.TempDir(), "command")
			body := "exit 0\n"
			if strings.HasPrefix(mode, "failed/") {
				body = "#!/nonexistent/foreground-start-interpreter\n"
			}
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			// The existing test executable's exit_0 mode implements the shell reached
			// by ENOEXEC fallback, without invoking the package test suite recursively.
			source := fmt.Sprintf("GOSH_CMD=exit_0 %q", script)
			f, err := syntax.NewParser().Parse(strings.NewReader(source), "")
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(ctx, f)
			if strings.HasPrefix(mode, "failed/") {
				if err == nil {
					t.Fatal("missing interpreter unexpectedly succeeded")
				}
			} else if err != nil {
				t.Fatalf("ENOEXEC fallback: %v", err)
			}
		}
		fd, err := unix.Open("/dev/tty", unix.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil || foreground != syscall.Getpgrp() {
			t.Fatalf("terminal not restored: foreground=%d owner=%d err=%v", foreground, syscall.Getpgrp(), err)
		}
		return
	}
	for _, kind := range []string{"start", "failed", "enoexec"} {
		for _, input := range []string{"tty", "redirected"} {
			t.Run(kind+"/"+input, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundCommandStartsWithTerminal$")
				cmd.Env = foregroundStartEnv(modeKey, kind+"/"+input)
				master, err := pty.Start(cmd)
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				output := make(chan []byte, 1)
				go func() { data, _ := io.ReadAll(master); output <- data }()
				err = cmd.Wait()
				master.Close()
				data := <-output
				if err != nil {
					t.Fatalf("owner subprocess: %v\n%s", err, data)
				}
			})
		}
	}
}

func foregroundStartEnv(key, value string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOSH_PROG=") || strings.HasPrefix(entry, "GOSH_CMD=") || strings.HasPrefix(entry, key+"=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, key+"="+value)
}
