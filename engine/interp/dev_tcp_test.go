// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package interp_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestDevTCPRedirects exercises Bash's redirection-only network pseudo-path
// against a real loopback listener. In particular, the <> case proves that a
// descriptor opened by exec remains bidirectional for later <&N and >&N uses.
func TestDevTCPRedirects(t *testing.T) {
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangBashPP, syntax.LangBats} {
		t.Run(lang.String(), func(t *testing.T) {
			listener := devTCPListener(t)
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil && line != "ping\n" {
					err = fmt.Errorf("got %q, want ping\\n", line)
				}
				if err == nil {
					_, err = fmt.Fprint(conn, "pong\n")
				}
				done <- err
			}()

			var out concBuffer
			script := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d\nprintf 'ping\\n' >&3\nIFS= read -r reply <&3\nprintf '<%%s>\\n' \"$reply\"\nexec 3>&-", devTCPPort(t, listener))
			devTCPRun(t, lang, script, &out)
			if got, want := out.String(), "<pong>\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(runnerRunTimeout):
				t.Fatal("loopback server did not finish")
			}
		})
	}
}

func TestDevTCPRedirectsBashPosixMode(t *testing.T) {
	listener := devTCPListener(t)
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err == nil && line != "posix\n" {
			err = fmt.Errorf("got %q, want posix\\n", line)
		}
		done <- err
	}()

	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(fmt.Sprintf("set -o posix\nprintf 'posix\\n' >/dev/tcp/127.0.0.1/%d", devTCPPort(t, listener))), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(interp.StdIO(nil, &concBuffer{}, &concBuffer{}), interp.WithBashCompatErrors(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	devTCPWait(t, done)
}

func TestDevTCPStrictPOSIXOpensOrdinaryFile(t *testing.T) {
	listener := devTCPListener(t)
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	var out concBuffer
	script := fmt.Sprintf("if : >/dev/tcp/127.0.0.1/%d; then printf opened; else printf 'status=%%s\\n' \"$?\"; fi", devTCPPort(t, listener))
	devTCPRun(t, syntax.LangPOSIX, script, &out)
	if got := out.String(); !strings.Contains(got, "status=1\n") {
		t.Fatalf("output = %q, want ordinary redirection failure", got)
	}
	if got := out.String(); strings.Contains(got, "connect:") {
		t.Fatalf("output = %q, contains Bash network diagnostic", got)
	}
	select {
	case <-accepted:
		t.Fatal("strict POSIX redirection connected to listener")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDevTCPReadAndWriteRedirects(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		listener := devTCPListener(t)
		defer listener.Close()
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				defer conn.Close()
				line, readErr := bufio.NewReader(conn).ReadString('\n')
				if readErr != nil {
					err = readErr
				} else if line != "write-only\n" {
					err = fmt.Errorf("got %q, want write-only\\n", line)
				}
			}
			done <- err
		}()
		devTCPRun(t, syntax.LangBash, fmt.Sprintf("printf 'write-only\\n' >/dev/tcp/127.0.0.1/%d", devTCPPort(t, listener)), nil)
		devTCPWait(t, done)
	})

	t.Run("read", func(t *testing.T) {
		listener := devTCPListener(t)
		defer listener.Close()
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				defer conn.Close()
				_, err = fmt.Fprint(conn, "read-only\n")
			}
			done <- err
		}()
		var out concBuffer
		devTCPRun(t, syntax.LangBash, fmt.Sprintf("IFS= read -r line </dev/tcp/127.0.0.1/%d\nprintf '<%%s>\\n' \"$line\"", devTCPPort(t, listener)), &out)
		if got, want := out.String(), "<read-only>\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		devTCPWait(t, done)
	})
}

// Bash exposes the companion /dev/udp pseudo-path through the same
// redirection contract. A datagram write is enough to verify that it stays on
// the pure-Go dial path without giving TCP-specific behavior to UDP.
func TestDevUDPWriteRedirect(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, portText, err := net.SplitHostPort(server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, _, err := server.ReadFrom(buf)
		if err == nil && string(buf[:n]) != "udp" {
			err = fmt.Errorf("got %q, want udp", buf[:n])
		}
		done <- err
	}()
	devTCPRun(t, syntax.LangBash, fmt.Sprintf("printf udp >/dev/udp/127.0.0.1/%d", port), nil)
	devTCPWait(t, done)
}

func TestDevTCPClosedPortSelectsConditionalAndDiagnoses(t *testing.T) {
	listener := devTCPListener(t)
	port := devTCPPort(t, listener)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var out concBuffer
	devTCPRun(t, syntax.LangBash, fmt.Sprintf("if : >/dev/tcp/127.0.0.1/%d; then printf opened; else printf 'closed=%%s\\n' \"$?\"; fi", port), &out)
	got := out.String()
	if !strings.Contains(got, "closed=1\n") {
		t.Fatalf("conditional output = %q, want failed branch", got)
	}
	if !strings.Contains(got, "connect: Connection refused\n") || !strings.Contains(got, fmt.Sprintf("/dev/tcp/127.0.0.1/%d: Connection refused\n", port)) {
		t.Fatalf("diagnostic = %q, want Bash connect diagnostics", got)
	}
}

// A canceled context must reach net.DialContext rather than leave a redirection
// attempt behind. The conditional gets the ordinary redirection-failure status.
func TestDevTCPCanceledContext(t *testing.T) {
	listener := devTCPListener(t)
	defer listener.Close()
	port := devTCPPort(t, listener)
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()
	file, err := syntax.NewParser().Parse(strings.NewReader(fmt.Sprintf("if : >/dev/tcp/127.0.0.1/%d; then printf opened; else printf canceled; fi", port)), "")
	if err != nil {
		t.Fatal(err)
	}
	var out concBuffer
	r, err := interp.New(interp.StdIO(nil, &out, &out), interp.WithBashCompatErrors(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx, file); err != nil && err != context.Canceled {
		t.Fatal(err)
	}
	select {
	case <-accepted:
		t.Fatal("canceled redirection still connected to listener")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDevTCPIsRedirectionOnly(t *testing.T) {
	listener := devTCPListener(t)
	defer listener.Close()
	port := devTCPPort(t, listener)
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()
	var out concBuffer
	devTCPRun(t, syntax.LangBash, fmt.Sprintf("source /dev/tcp/127.0.0.1/%d\nprintf 'status=%%s\\n' \"$?\"", port), &out)
	if !strings.Contains(out.String(), "status=1\n") {
		t.Fatalf("source output = %q, want ordinary failed file open", out.String())
	}
	select {
	case <-accepted:
		t.Fatal("non-redirection open connected to listener")
	case <-time.After(100 * time.Millisecond):
	}
}

func devTCPListener(t testing.TB) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func devTCPPort(t testing.TB, listener net.Listener) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func devTCPRun(t testing.TB, lang syntax.LangVariant, script string, out *concBuffer) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		out = &concBuffer{}
	}
	r, err := interp.New(interp.Lang(lang), interp.StdIO(nil, out, out), interp.WithBashCompatErrors(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
}

func devTCPWait(t testing.TB, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(runnerRunTimeout):
		t.Fatal("loopback server did not finish")
	}
}
