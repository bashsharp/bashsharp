package shellexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestNativeTaskMapfileUsesInterpreterInputPolicy(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	var out, diagnostic taskPolicyOutput
	session, err := shellrt.NewSession(shellrt.WithStdio(read, &out, &diagnostic), shellrt.WithShellFactory(New(BashPP())))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	task := session.Go(func(ctx context.Context, child *shellrt.Session) error {
		if err := child.Shell(ctx, "mapfile values"); err != nil {
			return err
		}
		if child.Status() != 2 {
			return errors.New("mapfile accepted nonregular task input")
		}
		return nil
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" || diagnostic.String() != "mapfile: blocking non-regular input is unavailable inside a Bash++ task\n" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostic.String())
	}
}

func TestNativeTaskReadCancellationAndExitTrapPolicy(t *testing.T) {
	for _, source := range []string{"read value", "trap 'mapfile values' EXIT"} {
		t.Run(source, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			var out, diagnostic taskPolicyOutput
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			session, err := shellrt.NewSession(shellrt.WithContext(ctx), shellrt.WithStdio(read, &out, &diagnostic), shellrt.WithShellFactory(New(BashPP())))
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			task := session.Go(func(ctx context.Context, child *shellrt.Session) error { return child.Shell(ctx, source) })
			err = task.Wait()
			if source == "read value" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("read err=%v diagnostic=%q", err, diagnostic.String())
				}
			} else if diagnostic.String() != "mapfile: blocking non-regular input is unavailable inside a Bash++ task\n" {
				t.Fatalf("EXIT trap policy lost: %q", diagnostic.String())
			}
		})
	}
}

type taskPolicyOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *taskPolicyOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *taskPolicyOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestNativeTaskMapfileAllowsRegularInput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lines")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("first\nsecond\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic taskPolicyOutput
	session, err := shellrt.NewSession(shellrt.WithStdio(file, &out, &diagnostic), shellrt.WithShellFactory(New(BashPP())))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	task := session.Go(func(ctx context.Context, child *shellrt.Session) error {
		if err := child.Shell(ctx, `mapfile -t values; printf '%s\n' "${values[@]}"`); err != nil {
			return err
		}
		if child.Status() != 0 {
			return errors.New("regular task input rejected")
		}
		return nil
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "first\nsecond\n" || diagnostic.String() != "" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostic.String())
	}
}
