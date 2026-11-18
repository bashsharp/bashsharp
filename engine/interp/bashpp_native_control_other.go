//go:build !unix

package interp

import "net"

func bashPPNativeControlListener() (net.Listener, string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	return listener, "tcp", func() {}, err
}
