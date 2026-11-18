// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// StartProcessSignalServer makes this process reachable by a sibling bashy's
// `kill`. Windows has no kill(2), so bashy processes signal each other over a
// per-process named pipe, \\.\pipe\bashy-sig-<pid>: a sender opens it, writes
// the decimal signal number and a newline, and closes; the server raises the
// number on the in-process signal bus, which runs the receiving shell's trap
// or, with neither trap nor ignore, its process default action (see
// [SetProcessSignalDefault]). The pipe is inbound-only, message-typed and
// served by one goroutine, one connection at a time; a sender that finds the
// pipe busy retries briefly.
//
// Contract for the standalone CLI: call it once at startup, before running
// any script, and defer the returned stop function; also install a default
// action with [SetProcessSignalDefault] that exits with
// [SignalMarkerExitCode] so a bashy parent reports the death as 128+signal.
// An embedding host that does not want to be signalled simply never calls
// it, and `kill` from another bashy then falls back to TerminateProcess.
//
// Off Windows the function is a no-op returning a nil error: the kernel
// delivers signals there.
func StartProcessSignalServer() (stop func(), err error) {
	srv := &signalServer{name: signalPipeName(os.Getpid())}
	if err := srv.start(); err != nil {
		return nil, err
	}
	return srv.stop, nil
}

type signalServer struct {
	name    string
	stopped atomic.Bool
	done    chan struct{}
	once    sync.Once
}

const (
	signalPipeBufferSize = 64
	signalServerStopWait = 2 * time.Second
)

func (s *signalServer) start() error {
	// Create the first instance synchronously so a caller learns at startup
	// whether the pipe name can be claimed at all.
	h, err := s.listen()
	if err != nil {
		return err
	}
	s.done = make(chan struct{})
	go s.serve(h)
	return nil
}

// listen creates one listening pipe instance.
func (s *signalServer) listen() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(s.name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateNamedPipe(name,
		windows.PIPE_ACCESS_INBOUND,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		signalPipeBufferSize, signalPipeBufferSize, 0, nil)
}

// serve accepts one connection at a time: connect, read one message, raise,
// disconnect. A new instance is created for each connection so the pipe
// name stays claimed for the life of the server.
func (s *signalServer) serve(h windows.Handle) {
	defer close(s.done)
	for {
		if h == windows.InvalidHandle {
			var err error
			if h, err = s.listen(); err != nil {
				return
			}
		}
		err := windows.ConnectNamedPipe(h, nil)
		if err != nil && err != windows.ERROR_PIPE_CONNECTED {
			windows.CloseHandle(h)
			h = windows.InvalidHandle
			if s.stopped.Load() {
				return
			}
			continue
		}
		if s.stopped.Load() {
			windows.CloseHandle(h)
			return
		}
		var buf [signalPipeBufferSize]byte
		var n uint32
		if err := windows.ReadFile(h, buf[:], &n, nil); err == nil {
			if num, ok := decodeSignalMessage(buf[:n]); ok {
				processSignalBus.raise(num)
			}
		}
		windows.DisconnectNamedPipe(h)
		windows.CloseHandle(h)
		h = windows.InvalidHandle
	}
}

// stop ends the server. ConnectNamedPipe blocks synchronously, so stop wakes
// the loop by connecting to the pipe itself, then waits briefly for it.
func (s *signalServer) stop() {
	s.once.Do(func() {
		s.stopped.Store(true)
		if name, err := windows.UTF16PtrFromString(s.name); err == nil {
			if h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0); err == nil {
				windows.CloseHandle(h)
			}
		}
		select {
		case <-s.done:
		case <-time.After(signalServerStopWait):
		}
	})
}
