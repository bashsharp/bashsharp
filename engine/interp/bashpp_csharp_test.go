package interp_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// S358.5 — a C# fence compiled through the pinned PowerShell's Add-Type: typed
// calls, the err binding, and an unaliased fence's bare call. The worker comes
// from discovery over the project PATH, never ambient state.
const cSharpFenceSource = `~~~csharp as cs
using System;
using System.Linq;

public static long Square(long x) => x * x;
public static string Join(string sep, long n) => string.Join(sep, Enumerable.Range(1, (int)n));
public static double Half(double x) => x / 2;
public static bool Even(long n) => n % 2 == 0;
public static string Fail(string why) => throw new InvalidOperationException(why);
~~~
`

func TestBashPPCSharpTypedCallsAndErrorBinding(t *testing.T) {
	dir, environ := powerShellProject(t)
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, cSharpFenceSource+`sq := cs.Square(6)
echo "square=$sq"
joined := cs.Join("-", 3)
echo "join=$joined"
five := 5.0
half := cs.Half(five)
echo "half=$half"
even := cs.Even(4)
echo "even=$even"
value, callErr := cs.Square(7)
echo "ok=$value:${callErr:+error}"
failed, failErr := cs.Fail("nope")
echo "fail=$failed:${failErr:+error}"
[[ $failErr == *InvalidOperationException*nope* ]] && echo "message=nope"
`)
	if status != 0 {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	want := "square=36\njoin=1-2-3\nhalf=2.5\neven=true\nok=49:\nfail=:error\nmessage=nope\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q (stderr %q)", stdout, want, stderr)
	}
}

func TestBashPPCSharpUnaliasedBareCall(t *testing.T) {
	dir, environ := powerShellProject(t)
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, "~~~cs\npublic static long Cube(long x) => x * x * x;\n~~~\ncube := Cube(3)\necho \"cube=$cube\"\n")
	if status != 0 || stdout != "cube=27\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

// An uncaught exception without an err binding fails the call with the
// exception type and message.
func TestBashPPCSharpUncaughtException(t *testing.T) {
	dir, environ := powerShellProject(t)
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, cSharpFenceSource+"cs.Fail(\"boom\")\necho \"after=$?\"\n")
	if !strings.Contains(stderr, "boom") || !strings.Contains(stderr, "InvalidOperationException") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

// Compilation-unit declarations are helpers, not exports: the shell sees only
// Run, with its ordinary result/error binding contract unchanged.
func TestBashPPCSharpCompilationUnitUnsafeAndExtension(t *testing.T) {
	dir, environ := powerShellProject(t)
	source := `~~~csharp as cs
using Helpers;
namespace One { public class Person { public int Value => 10; } }
namespace Two { public class Person { public int Value => 20; } }
namespace Helpers {
    public static class Ext { public static int Twice(this int value) => value * 2; }
}
public static unsafe int Run() {
    int value = new One.Person().Value + new Two.Person().Value;
    int* pointer = &value;
    return (*pointer).Twice();
}
~~~
value, callErr := cs.Run()
echo "value=$value:${callErr:+error}"
`
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, source)
	if status != 0 || stdout != "value=60:\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}
