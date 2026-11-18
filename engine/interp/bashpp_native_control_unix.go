//go:build unix

package interp

import (
	"net"
	"os"
	"path/filepath"
)

// bashPPNativeControlListener uses a local-domain socket for the high-frequency
// dependency control channel. Unlike TCP, it does not pay the network stack's
// packet and acknowledgement costs for each synchronous callback round trip.
func bashPPNativeControlListener() (net.Listener, string, func(), error) {
	dir, err := os.MkdirTemp("", "bashpp-control-")
	if err != nil {
		return bashPPNativeTCPControlListener()
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		cleanup()
		return bashPPNativeTCPControlListener()
	}
	return listener, "unix", cleanup, nil
}

func bashPPNativeTCPControlListener() (net.Listener, string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	return listener, "tcp", func() {}, err
}
