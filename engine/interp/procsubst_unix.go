// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"fmt"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

type procSubstFIFO struct {
	fifoPath string
}

// newProcSubstPipe creates the FIFO backing one process substitution.
//
// We can't atomically create a random unused temporary FIFO. Similar to
// [os.CreateTemp], keep trying new random paths until one does not exist.
// We use a uint64 because a uint32 easily runs into retries.
func (r *Runner) newProcSubstPipe(substWrites bool) (procSubstPipe, error) {
	var path string
	try := 0
	for {
		path = filepath.Join(r.tempDir, fifoNamePrefix+strconv.FormatUint(mathrand.Uint64(), 16))
		err := mkfifo(path, 0o666)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("cannot create fifo: %v", err)
		}
		if try++; try > 100 {
			return nil, fmt.Errorf("giving up at creating fifo: %v", err)
		}
	}
	return &procSubstFIFO{fifoPath: path}, nil
}

func (f *procSubstFIFO) path() string { return f.fifoPath }

// The substitution's end of the FIFO is opened as Bash opens it: blocking,
// paired by the kernel with whichever peer opens the other end — an external
// command or the shell's own redirection alike.

func (f *procSubstFIFO) openWriter() (*os.File, error) {
	return os.OpenFile(f.fifoPath, os.O_WRONLY, 0)
}

func (f *procSubstFIFO) openReader() (*os.File, error) {
	return os.OpenFile(f.fifoPath, os.O_RDONLY, 0)
}

func (f *procSubstFIFO) cleanup() {
	os.Remove(f.fifoPath)
}
