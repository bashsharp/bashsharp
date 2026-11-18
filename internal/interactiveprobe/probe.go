// Package interactiveprobe exercises the shell through a controlling Unix PTY
// or Windows ConPTY. It shares assertions between the CLI tests and native probe.
package interactiveprobe

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

type Probe struct{ Name, Input string }

// Cases deliberately bound the claim: registry operations are not TAB completion,
// and fc -e - is not an external interactive editor handoff.
var Cases = []Probe{
	{"compgen", `[[ $(compgen -W 'alpha beta' al) == alpha ]] && [[ $(compgen -W 'alpha beta' z; printf '%d' $?) == 1 ]]`},
	{"complete", `complete -W 'alpha beta' depthcmd && complete -p depthcmd > registry && IFS= read -r spec < registry && [[ $spec == "complete -W 'alpha beta' depthcmd" ]] && complete -r depthcmd && ! complete -p depthcmd 2>/dev/null`},
	{"history-memory", "set +o history\rhistory -c\rhistory -s alpha\rhistory -s beta\r" + `history -d 1 && [[ $(history) == *beta* && $(history) != *alpha* ]] && { history -c; [[ -z $(history) ]]; }`},
	{"history-file", "set +o history\rhistory -c\rhistory -s alpha\r" + `history -w saved && history -c && history -r saved && [[ $(history) == *alpha* ]] && { history -s beta; history -a saved; history -c; history -r saved; [[ $(history) == *alpha* && $(history) == *beta* ]]; }`},
	{"history-new", "set +o history\rhistory -c\r" + `printf 'alpha\n' > imported; history -r imported; printf 'beta\n' >> imported; history -n imported; [[ $(history) == *beta* ]]`},
	{"history-print", "set +o history\rhistory -c\rhistory -s 'echo alpha'\r" + `[[ $(history -p '!!') == 'echo alpha' ]]`},
	{"fc-list", "set +o history\rhistory -c\rhistory -s 'echo alpha'\rhistory -s 'echo beta'\r" + `[[ $(fc -ln 1 2) == $'\t echo alpha\n\t echo beta' && $(fc -lnr 1 2) == $'\t echo beta\n\t echo alpha' ]]`},
	{"fc-execute", "set +o history\rhistory -c\rhistory -s 'DEPTH_VALUE=old'\r" + `fc -s old=new 1 && [[ $DEPTH_VALUE == new ]]`},
	{"fc-editor-dash", "set +o history\rhistory -c\rhistory -s 'DEPTH_VALUE=edited'\r" + `fc -e - 1 && [[ $DEPTH_VALUE == edited ]]`},
	{"readline-kill-line", "garbage\x15[[ $- == *i* && -t 0 ]]"},
}

var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// ConPTY may encode CRLF as an absolute cursor move to column one.
var lineStart = regexp.MustCompile(`\x1b\[[0-9]+;1[Hf]`)

func clean(s string) string {
	s = lineStart.ReplaceAllString(s, "\n")
	return ansi.ReplaceAllString(strings.ReplaceAll(s, "\r", ""), "")
}

func ready(s string) bool { return strings.Contains(clean(s), "DEPTH_READY>") }

func status(s, name string) (int, bool) {
	lines := strings.Split(clean(s), "\n")
	for _, line := range lines[:len(lines)-1] {
		if n, err := strconv.Atoi(strings.TrimPrefix(line, "__DEPTH_"+name+":")); strings.HasPrefix(line, "__DEPTH_"+name+":") && err == nil {
			return n, true
		}
	}
	return 0, false
}

// Run uses an isolated home/history and working directory. Errors include the
// transcript; a timeout, wrong status, or failed child exit is always a failure.
func Run(shell string, x Probe) (string, error) {
	dir, err := os.MkdirTemp("", "bashy-depth-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	p, err := pty.New()
	if err != nil {
		return "", err
	}
	defer p.Close()
	if err = p.Resize(240, 40); err != nil {
		return "", err
	}
	c := p.Command(shell, "--noprofile", "--norc", "-i")
	c.Dir = dir
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		switch key {
		case "HOME", "USERPROFILE", "HISTFILE", "HISTCONTROL", "HISTIGNORE", "HISTSIZE", "HISTFILESIZE", "INPUTRC", "ENV", "BASH_ENV", "PS1", "PS2", "PROMPT_COMMAND", "SHELLOPTS", "BASHOPTS", "POSIXLY_CORRECT", "POSIX_PEDANTIC", "TERM":
			continue
		}
		c.Env = append(c.Env, entry)
	}
	c.Env = append(c.Env, "HOME="+dir, "USERPROFILE="+dir, "HISTFILE="+dir+"/history", "INPUTRC="+dir+"/inputrc", "TERM=xterm", "PS1=DEPTH_READY> ", "PS2=DEPTH_MORE> ")
	if err = c.Start(); err != nil {
		return "", err
	}
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	defer func() { _ = c.Process.Kill() }()
	chunks := make(chan []byte, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, e := p.Read(buf)
			if n > 0 {
				select {
				case chunks <- bytes.Clone(buf[:n]):
				case <-done:
					// Keep draining until p.Close unblocks the reader.
				}
			}
			if e != nil {
				return
			}
		}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var out strings.Builder
	sent, exiting := false, false
	queries := 0
	for {
		select {
		case b, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			out.Write(b)
			// Act as a terminal emulator for readline's optional cursor-position query.
			for n := strings.Count(out.String(), "\x1b[6n"); queries < n; queries++ {
				if _, err = p.Write([]byte("\x1b[1;1R")); err != nil {
					return out.String(), err
				}
			}
			if !sent && ready(out.String()) {
				sent = true
				input := x.Input + `; printf '\n%s%s:%d\n' '__DEPTH_' '` + x.Name + `' "$?"` + "\r"
				if _, err = p.Write([]byte(input)); err != nil {
					return out.String(), err
				}
			}
			if n, ok := status(out.String(), x.Name); sent && ok && !exiting {
				if n != 0 {
					return out.String(), fmt.Errorf("assertion status %d", n)
				}
				exiting = true
				if _, err = p.Write([]byte("exit 0\r")); err != nil {
					return out.String(), err
				}
			}
		case err = <-waited:
			if err != nil || !exiting {
				return out.String(), fmt.Errorf("shell exit before successful completion: %v", err)
			}
			return out.String(), nil
		case <-timer.C:
			return out.String(), fmt.Errorf("interactive probe timeout")
		}
	}
}
