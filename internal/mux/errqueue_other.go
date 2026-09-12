//go:build !linux

package mux

import "net"

// The error queue is a Linux interface. The helper runs on Linux only; on
// another platform a socket error ends the flow.

func enableRecvErr(net.PacketConn, bool) error { return nil }

func isICMPErrno(error) bool { return false }

func drainErrQueue(net.PacketConn, bool) []ICMPError { return nil }
