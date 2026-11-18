// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import "io"

// pipelineWriter keeps platform-specific SIGPIPE handling at the simulated
// process boundary and covers both stdout and stderr under |&. The default
// exec handler unwraps it so real child processes retain native SIGPIPE.
type pipelineWriter struct {
	w      io.Writer
	runner *Runner
}

func (w *pipelineWriter) Write(p []byte) (int, error) {
	n, err := writePipelineOutput(w.w, p)
	if isBrokenPipeWriteErr(err) {
		w.runner.pipelineWriteBroken.Store(true)
	}
	return n, err
}

func unwrapPipelineWriter(w io.Writer) io.Writer {
	if pw, ok := w.(*pipelineWriter); ok {
		return pw.w
	}
	return w
}

// rebindPipelineWriters makes a cloned runner own EPIPE from its own writes.
// Preserve stdout==stderr aliasing for |& while sharing the same kernel pipe.
func rebindPipelineWriters(r *Runner) {
	out, outOK := r.stdout.(*pipelineWriter)
	errOut, errOK := r.stderr.(*pipelineWriter)
	if outOK {
		rebound := &pipelineWriter{w: out.w, runner: r}
		r.stdout = rebound
		if errOK && errOut == out {
			r.stderr = rebound
			errOK = false
		}
	}
	if errOK {
		r.stderr = &pipelineWriter{w: errOut.w, runner: r}
	}
}

func (r *Runner) applyPipelineWriteFailure() {
	if !r.pipelineWriteBroken.Swap(false) {
		return
	}
	if r.pipelineSIGPIPEIgnored() {
		return
	}
	r.exit.code = 128 + 13
	r.exit.exiting = true
	if n := len(r.pipeStatus); n > 0 {
		r.pipeStatus[n-1] = "141"
	}
}
