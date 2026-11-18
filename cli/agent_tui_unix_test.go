//go:build unix

package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty/v2"
)

// TestAgentTUINativeOverPTY is the A7 acceptance (Sprint #387): `bashy ycode`
// on a terminal hosts ycode's native TUI (not bashy's interactive shell), a
// literal line runs on the bashy binary through ycodecli.LiteralShell (its
// parent is the TUI process, not the in-process interpreter), and free text
// becomes a turn that reaches the model.
func TestAgentTUINativeOverPTY(t *testing.T) {
	bin := builtBashyBin(t)
	config, err := filepath.Abs("../../../ycode/examples/agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Skipf("the ycode sibling is not mounted: %v", err)
	}

	// The model endpoint records what reaches it and answers every request
	// with one fixed text (the Responses stream).
	var mu sync.Mutex
	var requests []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"STUB-ANSWER\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer model.Close()
	reached := func(text string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, body := range requests {
			if strings.Contains(body, text) {
				return true
			}
		}
		return false
	}

	home := t.TempDir()
	cmd := exec.Command(bin, "ycode", "-f", config)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"HOME=" + home, "BASHY_HOME=" + filepath.Join(home, ".bashy"),
		"PATH=" + filepath.Dir(bin) + ":/bin:/usr/bin", "TERM=xterm-256color",
		"OPENAI_BASE_URL=" + model.URL, "OPENAI_API_KEY=stub", "BASHY_HINTS=off",
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	capture := startPTYCapture(ptmx)
	t.Cleanup(func() {
		_ = ptmx.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	const wait = 20 * time.Second
	// The native TUI greets with its leave hint; bashy's shell would show a
	// prompt carrying "[ycode SESSION]" instead.
	first := capture.waitFor(t, []byte("Ctrl-D leaves"), wait)
	if regexp.MustCompile(`\[ycode [0-9a-f]{8}\]`).Match(first) {
		t.Fatalf("bashy's in-shell agent terminal answered, not the native TUI: %q", first)
	}
	typeLine := func(line string) int {
		t.Helper()
		offset := capture.len()
		if _, err := io.WriteString(ptmx, line+"\r"); err != nil {
			t.Fatal(err)
		}
		return offset
	}

	// Rung 0: a literal line runs as typed, on a bashy child of the TUI.
	at := typeLine("echo literal-$((40+2)) ppid=$PPID")
	want := fmt.Sprintf("literal-42 ppid=%d", cmd.Process.Pid)
	capture.waitForFrom(t, at, []byte(want), wait)
	// The TUI takes the terminal back (its status line repaints) before the
	// next line is typed; typed earlier, it lands in the cooked tty.
	capture.waitForFrom(t, capture.len(), []byte("session "), wait)

	// Last rung: free text becomes an agent turn.
	at = typeLine("what does this repo do?")
	capture.waitForFrom(t, at, []byte("STUB-ANSWER"), wait)
	capture.waitForFrom(t, at, []byte("turn ended in"), wait)
	if !reached("what does this repo do?") {
		t.Fatalf("free text never reached the model; requests: %q", requests)
	}

	if os.Getenv("AGENT_TUI_TRANSCRIPT") != "" {
		capture.mu.Lock()
		t.Logf("transcript:\n%s", capture.buf.String())
		capture.mu.Unlock()
	}
	if _, err := io.WriteString(ptmx, "/quit\r"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bashy ycode exit: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("the native TUI did not exit on /quit")
	}
}
