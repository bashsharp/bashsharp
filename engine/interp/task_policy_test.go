package interp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func taskPolicyFile(t *testing.T, source string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "input.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestHostedTaskPolicyMapfileAndScopeRestoration(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	var out, diagnostic bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), StdIO(read, &out, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = runner.Run(WithTaskPolicy(ctx), taskPolicyFile(t, "mapfile values\n"))
	var exit ExitStatus
	if !errors.As(err, &exit) || exit != 2 || out.Len() != 0 || diagnostic.String() != "mapfile: blocking non-regular input is unavailable inside a Bash++ task\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out.String(), diagnostic.String(), err)
	}
	if ctx.Err() != nil {
		t.Fatal("policy waited for pipe data")
	}
	if runner.bashPPGoTask || runner.bashPPHostedTask || runner.bashPPConcurrent != nil {
		t.Fatal("policy leaked ownership or scope")
	}
	// The same runner remains an ordinary owner without the context policy.
	if _, err := write.WriteString("owner\n"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	out.Reset()
	diagnostic.Reset()
	if err := runner.Run(context.Background(), taskPolicyFile(t, "mapfile -t values\necho \"${values[0]}\"\n")); err != nil || out.String() != "owner\n" || diagnostic.Len() != 0 {
		t.Fatalf("owner stdout=%q stderr=%q err=%v", out.String(), diagnostic.String(), err)
	}
}

func TestHostedTaskPolicyKeepsSourceDescendantOwnership(t *testing.T) {
	var out, diagnostic bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	source := "func nested() { echo child; }\ngo nested()\n"
	if err := runner.Run(WithTaskPolicy(context.Background()), taskPolicyFile(t, source)); err != nil || out.String() != "child\n" || diagnostic.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q err=%v", out.String(), diagnostic.String(), err)
	}
	if runner.bashPPConcurrent != nil {
		t.Fatal("host policy suppressed EOF task cleanup")
	}
}

func TestHostedTaskPolicyCancelsReadAndRejectsProcessEffects(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer read.Close()
		defer write.Close()
		var out, diagnostic bytes.Buffer
		runner, err := New(Lang(syntax.LangBashPP), StdIO(read, &out, &diagnostic))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		armed := false
		ctx = WithBlockingObserver(WithTaskPolicy(ctx), func() { armed = true })
		err = runner.Run(ctx, taskPolicyFile(t, "read value\n"))
		if !errors.Is(err, context.DeadlineExceeded) || !armed {
			t.Fatalf("read err=%v armed=%v diagnostic=%q", err, armed, diagnostic.String())
		}
	})
	t.Run("kill", func(t *testing.T) {
		var out, diagnostic bytes.Buffer
		runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &diagnostic))
		if err != nil {
			t.Fatal(err)
		}
		err = runner.Run(WithTaskPolicy(context.Background()), taskPolicyFile(t, "kill -l\n"))
		var exit ExitStatus
		if !errors.As(err, &exit) || exit != 2 || out.Len() != 0 || diagnostic.String() != "kill: process signals are unavailable inside a Bash++ task\n" {
			t.Fatalf("stdout=%q stderr=%q err=%v", out.String(), diagnostic.String(), err)
		}
	})
}
