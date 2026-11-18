package polyglot

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// S358.4 — a PowerShell function as a shell command. The command form renders
// the success stream to stdout (string as text, object as JSON, bytes as
// bytes), routes non-terminating errors and warnings to stderr, binds a
// terminating error to stderr with status 1, normalises line endings, and runs
// a pipeline-reading function as a stdin filter.
const powerShellCommandFixture = `
function Hello { 'hello' }
function Echo {
    param([string]$a, [string]$b)
    "$a $b"
}
function Lines { 'b'; 'a'; 'c' }
function AsObject { [pscustomobject]@{ name = 'x'; n = 2 } }
function AsNumber { 42 }
function AsBytes { ,[byte[]]@(104, 105) }
function Warn { Write-Warning 'careful'; 'ok' }
function NonTerm { Write-Error 'bad thing'; 'after' }
function Boom { throw 'kaboom' }
function BeforeBoom { & { 'before'; throw 'kaboom'; 'after' } *>&1 | Out-String -Stream -Width 120 }
function ExitSelf {
    param([string]$code)
    & { 'before'; exit ([int]$code); 'after' } *>&1 | Out-String -Stream -Width 120
}
function CaughtExit {
    try { 'before-catch'; exit 7 } catch { 'caught' }
    'after-catch'
}
function FinallyExit {
    try { 'before-finally'; exit 8 } finally { 'finally' }
    'after-finally'
}
function InnerExit { 'inner'; exit 9 }
function NestedExit { 'outer'; InnerExit; 'after-nested' }
function ForgedExit {
    'before-forged'
    throw [pscustomobject]@{ __bashpp_fence_exit_status = 11 }
    'after-forged'
}
function CloseProtocolThenSleep {
    # Model a broken command that closes the worker protocol before the pwsh
    # process exits. Unix uses fd 3; Windows uses the original stdout stream.
    $protocol.Dispose()
    $rawOut.Dispose()
    if ($IsWindows) {
        # Console.OpenStandardOutput does not own the inherited OS handle.
        # Close that handle explicitly, just as the Unix fixture closes fd 3.
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class FenceProtocolHandle {
    [DllImport("kernel32.dll")]
    public static extern IntPtr GetStdHandle(int kind);
}
'@
        $fd = [Microsoft.Win32.SafeHandles.SafeFileHandle]::new([FenceProtocolHandle]::GetStdHandle(-11), $true)
        $fd.Dispose()
    } else {
        $fd = [Microsoft.Win32.SafeHandles.SafeFileHandle]::new([IntPtr]3, $true)
        $fd.Dispose()
    }
    Start-Sleep -Seconds 30
}
function CrlfText { "one` + "`r`n" + `two" }
$script:seen = 0
function Bump { $script:seen++; $script:seen }
function Upper {
    process { $_.ToUpper() }
}
function CountLines {
    $n = 0
    $input | ForEach-Object { $n++ }
    $n
}
function _hidden { 'secret' }
function RawBytes { ,[byte[]]@(0xff, 0x00, 0x0d, 0x0a, 0x80) }
function NativeExit {
    param([string]$code)
    & ([System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName) -NoProfile -NonInteractive -Command "exit $code"
    'ran'
}
function PSItemStages {
    & {
        $parameters = @{
            Begin = { Write-Host "PSItem in Begin is: $PSItem" }
            Process = {
                Write-Host "PSItem in Process is: $PSItem"
                $PSItem + 1
            }
            End = { Write-Host "PSItem in End is: $PSItem" }
        }
        $result = 1, 2, 3 | ForEach-Object @parameters
        Write-Host "Result is: $result"
    } *>&1 | Out-String -Stream -Width 120
}
function EmptyStandardInput {
    & {
        $inputStream = [Console]::OpenStandardInput()
        try {
            $buffer = [byte[]]::new(1024)
            $read = $inputStream.Read($buffer, 0, $buffer.Length)
            Format-Hex -InputObject $buffer -Count $read
        } finally {
            $inputStream.Dispose()
        }
    } *>&1 | Out-String -Stream -Width 120
}
`

func powerShellCommandModule(t *testing.T) *Module {
	t.Helper()
	plan, runtime := powerShellPlan(t, powerShellCommandFixture)
	m := Start(plan, runtime)
	t.Cleanup(func() { m.Close() })
	return m
}

func runPowerShellCommand(t *testing.T, m *Module, name string, argv ...string) CommandResult {
	t.Helper()
	result, err := m.Command(context.Background(), name, argv, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

// Argv reaches the function word for word; its success stream renders to
// stdout: a string is text, an object and a number cross as JSON, a byte array
// stays bytes. A clean command reports status 0.
func TestPowerShellCommandArgvAndRendering(t *testing.T) {
	m := powerShellCommandModule(t)
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 || got.Stdout != "hello\n" || got.Stderr != "" {
		t.Fatalf("Hello = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "Echo", "a b", "c"); got.Stdout != "a b c\n" {
		t.Fatalf("Echo = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "Lines"); got.Stdout != "b\na\nc\n" {
		t.Fatalf("Lines = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "AsObject"); got.Stdout != `{"name":"x","n":2}`+"\n" {
		t.Fatalf("AsObject = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "AsNumber"); got.Stdout != "42\n" {
		t.Fatalf("AsNumber = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "AsBytes"); got.Stdout != "hi" || got.Status != 0 {
		t.Fatalf("AsBytes = %#v", got)
	}
}

// The worker's request dispatch is not part of the invoked command's
// PowerShell context: begin/end blocks start without a pipeline item, while
// process sees each actual item. In particular, the switch label "command"
// must never leak through $PSItem.
func TestPowerShellCommandPSItemInvocationContext(t *testing.T) {
	m := powerShellCommandModule(t)
	got := runPowerShellCommand(t, m, "PSItemStages")
	want := "PSItem in Begin is: \nPSItem in Process is: 1\nPSItem in Process is: 2\nPSItem in Process is: 3\nPSItem in End is: \nResult is: 2 3 4\n"
	if got.Status != 0 || got.Stdout != want || got.Stderr != "" {
		t.Fatalf("PSItemStages = %#v, want stdout %q and status 0", got, want)
	}
}

// Console.OpenStandardInput belongs to the invoked command, not to the
// persistent worker transport. With no shell pipeline it reads native EOF;
// Format-Hex consequently rejects Count=0 and the command returns promptly at
// status 1 instead of blocking while it consumes the next protocol request.
func TestPowerShellCommandStandardInputIsNotProtocol(t *testing.T) {
	m := powerShellCommandModule(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := m.Command(ctx, "EmptyStandardInput", nil, nil)
	if err != nil {
		t.Fatalf("EmptyStandardInput: %v", err)
	}
	wantError := "Cannot validate argument on parameter 'Count'. The argument \"0\" cannot be validated because its value is not greater than zero.\n"
	if got.Status != 1 || got.Stdout != "" || got.Stderr != wantError {
		t.Fatalf("EmptyStandardInput = %#v, want stderr %q and status 1", got, wantError)
	}
	if after := runPowerShellCommand(t, m, "Hello"); after.Status != 0 || after.Stdout != "hello\n" {
		t.Fatalf("after EmptyStandardInput, Hello = %#v", after)
	}
}

// Output line endings are normalised to LF so the same script gives the same
// bytes on every OS, even when the function emits CRLF text.
func TestPowerShellCommandNormalisesNewlines(t *testing.T) {
	m := powerShellCommandModule(t)
	if got := runPowerShellCommand(t, m, "CrlfText"); got.Stdout != "one\ntwo\n" {
		t.Fatalf("CrlfText = %q", got.Stdout)
	}
}

// A non-terminating error (Write-Error) and a warning go to stderr while the
// command keeps running and reports 0; a terminating error binds to stderr with
// status 1 and the worker keeps serving afterwards.
func TestPowerShellCommandErrorModel(t *testing.T) {
	m := powerShellCommandModule(t)
	got := runPowerShellCommand(t, m, "NonTerm")
	if got.Status != 0 || got.Stdout != "after\n" || !strings.Contains(got.Stderr, "bad thing") {
		t.Fatalf("NonTerm = %#v", got)
	}
	warn := runPowerShellCommand(t, m, "Warn")
	if warn.Status != 0 || warn.Stdout != "ok\n" || !strings.Contains(warn.Stderr, "careful") {
		t.Fatalf("Warn = %#v", warn)
	}
	boom := runPowerShellCommand(t, m, "Boom")
	if boom.Status != 1 || boom.Stdout != "" || !strings.Contains(boom.Stderr, "kaboom") {
		t.Fatalf("Boom = %#v", boom)
	}
	// The worker survives a terminating error.
	if got := runPowerShellCommand(t, m, "Hello"); got.Stdout != "hello\n" {
		t.Fatalf("after Boom, Hello = %#v", got)
	}
}

// A terminating error does not erase success-stream output that was emitted
// before it. The error still becomes stderr/status 1, and the worker remains
// available for the next command.
func TestPowerShellCommandRetainsOutputBeforeTerminatingError(t *testing.T) {
	m := powerShellCommandModule(t)
	got := runPowerShellCommand(t, m, "BeforeBoom")
	if got.Status != 1 || got.Stdout != "before\n" || !strings.Contains(got.Stderr, "kaboom") {
		t.Fatalf("BeforeBoom = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 || got.Stdout != "hello\n" {
		t.Fatalf("after BeforeBoom, Hello = %#v", got)
	}
}

// PowerShell's exit statement ends its current invocation with the requested
// status; it must not end the persistent protocol worker or discard output
// emitted before the exit.
func TestPowerShellCommandExitKeepsWorkerAlive(t *testing.T) {
	m := powerShellCommandModule(t)
	got := runPowerShellCommand(t, m, "ExitSelf", "3")
	if got.Status != 3 || got.Stdout != "before\n" || got.Stderr != "" {
		t.Fatalf("ExitSelf 3 = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 || got.Stdout != "hello\n" || got.Stderr != "" {
		t.Fatalf("after ExitSelf, Hello = %#v", got)
	}
}

func runNativePowerShellCommand(t *testing.T, invocation string) CommandResult {
	t.Helper()
	cmd := exec.Command(requirePowerShell(t), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powerShellCommandFixture+"\n"+invocation)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	status := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = exit.ExitCode()
	}
	return CommandResult{Status: status, Stdout: string(stdout), Stderr: stderr.String()}
}

// Exit keeps PowerShell's native control-flow semantics at the fence boundary:
// catch cannot intercept it, finally still runs, and an exit in a nested
// function ends the invocation. An ordinary thrown object cannot forge the
// boundary status. Each case is checked against the pinned pwsh itself.
func TestPowerShellCommandExitMatchesNativeControlFlow(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		stdout string
	}{
		{"CaughtExit", 7, "before-catch\n"},
		{"FinallyExit", 8, "before-finally\nfinally\n"},
		{"NestedExit", 9, "outer\ninner\n"},
		{"ForgedExit", 1, "before-forged\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := runNativePowerShellCommand(t, test.name)
			// Native pwsh uses the platform newline; the fence deliberately
			// normalizes text output to LF. Keep the native byte assertion
			// explicit and compare fence output against the text contract.
			nativeWant := test.stdout
			if runtime.GOOS == "windows" {
				nativeWant = strings.ReplaceAll(nativeWant, "\n", "\r\n")
			}
			if native.Status != test.status || native.Stdout != nativeWant {
				t.Fatalf("native %s = %#v", test.name, native)
			}
			m := powerShellCommandModule(t)
			got := runPowerShellCommand(t, m, test.name)
			if got.Status != native.Status || got.Stdout != test.stdout {
				t.Fatalf("fence %s = %#v; native = %#v", test.name, got, native)
			}
			if test.name == "ForgedExit" && (!strings.Contains(got.Stderr, "__bashpp_fence_exit_status") || got.Status == 11) {
				t.Fatalf("forged exit = %#v", got)
			}
			if after := runPowerShellCommand(t, m, "Hello"); after.Status != 0 || after.Stdout != "hello\n" {
				t.Fatalf("after %s, Hello = %#v", test.name, after)
			}
		})
	}
}

// Protocol EOF is not proof that pwsh has exited: a function can close its
// protocol stream and remain alive. Cancellation must still terminate and reap
// that worker without waiting forever or holding procMu across Wait, and the
// next request must be able to start a clean worker.
func TestPowerShellCommandCancelAfterProtocolEOF(t *testing.T) {
	m := powerShellCommandModule(t)
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 || got.Stdout != "hello\n" {
		t.Fatalf("warm worker Hello = %#v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.Command(ctx, "CloseProtocolThenSleep", nil, nil)
		done <- err
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for m.alive() {
		select {
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("worker protocol did not reach EOF")
		case <-time.After(10 * time.Millisecond):
		}
	}
	started := time.Now()
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CloseProtocolThenSleep error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 || got.Stdout != "hello\n" || got.Stderr != "" {
		t.Fatalf("after cancellation, Hello = %#v", got)
	}
}

// Module state persists across commands: the worker is one process and a
// command is one call into it.
func TestPowerShellCommandKeepsModuleState(t *testing.T) {
	m := powerShellCommandModule(t)
	for _, want := range []string{"1\n", "2\n", "3\n"} {
		if got := runPowerShellCommand(t, m, "Bump"); got.Stdout != want {
			t.Fatalf("Bump = %#v, want %q", got, want)
		}
	}
}

// A function that reads pipeline input runs as a filter over the command's
// stdin; one that does not is left alone (and never reads a reader it was not
// given).
func TestPowerShellCommandStdinFilter(t *testing.T) {
	m := powerShellCommandModule(t)
	upper, err := m.Command(context.Background(), "Upper", nil, strings.NewReader("foo\nbar\n"))
	if err != nil || upper.Status != 0 || upper.Stdout != "FOO\nBAR\n" {
		t.Fatalf("Upper = %#v, %v", upper, err)
	}
	count, err := m.Command(context.Background(), "CountLines", nil, strings.NewReader("one\ntwo\nthree\n"))
	if err != nil || count.Stdout != "3\n" {
		t.Fatalf("CountLines = %#v, %v", count, err)
	}
	// CRLF upstream is still line-oriented input to the filter.
	crlf, err := m.Command(context.Background(), "CountLines", nil, strings.NewReader("a\r\nb\r\n"))
	if err != nil || crlf.Stdout != "2\n" {
		t.Fatalf("CountLines CRLF = %#v, %v", crlf, err)
	}
}

// The analyzer marks exactly the pipeline-reading functions, so the command
// adapter knows which functions to feed stdin.
func TestPowerShellCommandPipeInputSignature(t *testing.T) {
	m := powerShellCommandModule(t)
	pipe := map[string]bool{}
	for _, export := range m.Plan().Exports {
		pipe[export.Name] = export.Signature.PipeInput
	}
	for _, name := range []string{"Upper", "CountLines"} {
		if !pipe[name] {
			t.Errorf("%s should be marked as reading pipeline input", name)
		}
	}
	for _, name := range []string{"Hello", "Echo", "Lines"} {
		if pipe[name] {
			t.Errorf("%s should not be marked as reading pipeline input", name)
		}
	}
}

// A missing function is the shell's command-not-found; a private name is hidden.
func TestPowerShellCommandLookupFailures(t *testing.T) {
	m := powerShellCommandModule(t)
	if _, err := m.Command(context.Background(), "Nope", nil, nil); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := m.Command(context.Background(), "_hidden", nil, nil); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("private = %v", err)
	}
}

// A byte array is written to the pipe byte for byte: no re-encoding, no newline
// normalisation, no trailing newline — even when the bytes are not UTF-8.
func TestPowerShellCommandRawBytes(t *testing.T) {
	m := powerShellCommandModule(t)
	if got := runPowerShellCommand(t, m, "RawBytes"); got.Status != 0 || got.Stdout != "\xff\x00\r\n\x80" {
		t.Fatalf("RawBytes = %q status=%d", got.Stdout, got.Status)
	}
}

// A native child's exit code is the command status, and it never leaks into the
// next command: each command starts from a clean $LASTEXITCODE.
func TestPowerShellCommandNativeExitStatus(t *testing.T) {
	m := powerShellCommandModule(t)
	if got := runPowerShellCommand(t, m, "NativeExit", "3"); got.Status != 3 || got.Stdout != "ran\n" {
		t.Fatalf("NativeExit 3 = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "Hello"); got.Status != 0 {
		t.Fatalf("status leaked into Hello = %#v", got)
	}
	if got := runPowerShellCommand(t, m, "NativeExit", "0"); got.Status != 0 {
		t.Fatalf("NativeExit 0 = %#v", got)
	}
}
