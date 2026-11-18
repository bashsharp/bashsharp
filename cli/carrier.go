// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// carrierHelperArg is the argv[1] marker that turns this executable into a
// job-carrier helper process. The interpreter runs asynchronous lists as
// goroutines, so a pure-builtin `cmd &` has no kernel process of its own and
// `$!` would fall back to the synthetic "g<N>" handle — which external tools
// (and the POSIX certification suite: tetapi.sh does integer comparisons on
// tet_context=$!) cannot probe or signal. With interp.WithJobCarrier wired,
// the runner instead starts one helper per background job and uses its real
// PID as the job's identity; see internal/cli/carrier_unix.go.
const carrierHelperArg = "--bashy-job-carrier"
const carrierReadyEnv = "BASHY_JOB_CARRIER_READY_FD"
const carrierIgnoredEnv = "BASHY_JOB_CARRIER_IGNORED"

// MaybeRunJobCarrierHelper turns the current process into a job-carrier
// helper — a kernel-visible stand-in for one background job — when argv is
// exactly [argv0, "--bashy-job-carrier"]. It never returns in that case.
//
// It must run before flag parsing, startup files, AgentOS dispatch and
// telemetry: a carrier is pure process identity and must not execute shell or
// user code. cli.Main calls it first; cmd/bashy additionally calls it before
// telemetry.Init so a carrier never initializes the OTel plane.
//
// On Unix the platform hook relays catchable signals over the private
// readiness/event descriptor, including notify-only signals the Go runtime
// would otherwise consume; kernel stops and uncatchable termination remain
// wait states. Windows has no inheritable extra descriptor, so its hooks are
// empty and the carrier is a plain stand-in process (carrier_windows.go). On
// every platform its lifetime is bounded by the parent-owned stdin pipe: it
// exits on EOF, so it cannot outlive a parent shell that dies without reaping
// it.
func MaybeRunJobCarrierHelper() {
	if len(os.Args) != 2 || os.Args[1] != carrierHelperArg {
		return
	}
	// Install the helper relay before publishing readiness. Catchable signal
	// events remain private to the owning runner, which applies live traps.
	resetJobCarrierSignals()
	signalJobCarrierReady()
	buf := make([]byte, 128)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			os.Exit(0)
		}
	}
}

// newCLIJobCarrier is the seam through which newRunner obtains the OS-backed
// job carrier for the standalone CLI runner. It is a var so tests can inject
// failing carriers and prove the fail-closed path without a real platform
// carrier. Session/embedded runners (NewSessionRunner) deliberately do not
// consult it: they must never spawn processes their host did not ask for.
var newCLIJobCarrier = platformJobCarrier

// JobCarrier returns the OS-backed carrier for a bashy-owned runner that
// executes user scripts with background jobs. A nil result means bash-mode
// runners should retain their synthetic job handles on this platform.
func JobCarrier() interp.JobCarrier { return newCLIJobCarrier() }

// unsupportedJobCarrier is wired on platforms with no OS-backed carrier when
// the shell runs in POSIX/sh mode. Opting into WithJobCarrier is a strict
// contract — the runner never degrades to the synthetic g<N> identity — so a
// platform that cannot supply real PIDs must fail each background job closed
// rather than silently hand POSIX callers a handle that no external tool can
// probe or signal.
type unsupportedJobCarrier struct{}

func (unsupportedJobCarrier) StartCarrier(context.Context) (interp.CarrierProcess, error) {
	return nil, errors.New("background jobs need kernel-visible PIDs in POSIX mode, unsupported on " + runtime.GOOS)
}
