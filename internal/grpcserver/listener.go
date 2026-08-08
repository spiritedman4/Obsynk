// Package grpcserver hosts the ObsynkService gRPC server over a loopback TCP
// listener. Loopback TCP was chosen over Unix domain sockets / Windows named
// pipes after an early spike: @grpc/grpc-js (the Node client the Obsidian
// plugin uses) has no resolver for named pipe targets and cannot dial one at
// all, while a Windows named pipe server itself works fine (verified
// separately via .NET's NamedPipeClientStream). Loopback TCP is supported
// natively by both grpc-js and Go's grpc package on every platform, and
// binding 127.0.0.1 with an OS-assigned ephemeral port keeps the same
// "local machine only, one instance per vault, no fixed port to collide on"
// properties the socket/pipe approach was chosen for.
package grpcserver

import "net"

// Listen binds a loopback TCP listener on an OS-assigned free port. The
// caller (cmd/obsynkd) is responsible for publishing the resulting port
// (e.g. printed to stdout) so the Obsidian plugin can discover it after
// spawning the daemon.
func Listen() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}
