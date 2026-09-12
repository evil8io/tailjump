package mux

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// sockExtendedErrLen is the size of struct sock_extended_err. The offender
// address follows it in the control message.
const sockExtendedErrLen = 16

// enableRecvErr turns on IP_RECVERR or IPV6_RECVERR, so the kernel queues
// the ICMP errors for the socket's packets on the error queue instead of
// dropping the soft ones.
func enableRecvErr(pc net.PacketConn, v6 bool) error {
	sc, ok := pc.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		if v6 {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_RECVERR, 1)
		} else {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVERR, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

// isICMPErrno reports whether a socket error is one the kernel raises for
// a received ICMP error, which means the error queue has the details.
func isICMPErrno(err error) bool {
	for _, errno := range []syscall.Errno{
		unix.EHOSTUNREACH, unix.ENETUNREACH, unix.ECONNREFUSED,
		unix.EMSGSIZE, unix.EPROTO, unix.EACCES,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// drainErrQueue reads every entry of the socket's error queue and returns
// the ones that an ICMP message caused. The data of an entry is the part of
// the original packet the error quoted, after the transport header the
// kernel matched.
func drainErrQueue(pc net.PacketConn, v6 bool) []ICMPError {
	sc, ok := pc.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	var out []ICMPError
	buf := make([]byte, maxUDPFrame)
	oob := make([]byte, 512)
	_ = rc.Control(func(fd uintptr) {
		for {
			n, oobn, _, _, err := unix.Recvmsg(int(fd), buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
			if err != nil {
				return
			}
			if e, ok := parseErrQueue(oob[:oobn], buf[:n], v6); ok {
				out = append(out, e)
			}
		}
	})
	return out
}

func parseErrQueue(oob, data []byte, v6 bool) (ICMPError, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return ICMPError{}, false
	}
	for _, m := range msgs {
		isV4 := m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_RECVERR
		isV6 := m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_RECVERR
		if (!v6 && !isV4) || (v6 && !isV6) || len(m.Data) < sockExtendedErrLen {
			continue
		}
		origin := m.Data[4]
		if origin != unix.SO_EE_ORIGIN_ICMP && origin != unix.SO_EE_ORIGIN_ICMP6 {
			continue
		}
		from, ok := offenderAddr(m.Data[sockExtendedErrLen:], v6)
		if !ok {
			continue
		}
		return ICMPError{
			Type:  m.Data[5],
			Code:  m.Data[6],
			Info:  binary.NativeEndian.Uint32(m.Data[8:12]),
			From:  from,
			Inner: append([]byte(nil), data...),
		}, true
	}
	return ICMPError{}, false
}

// offenderAddr reads the sockaddr that follows sock_extended_err: a
// sockaddr_in with the address at offset 4, or a sockaddr_in6 with the
// address at offset 8.
func offenderAddr(b []byte, v6 bool) (netip.Addr, bool) {
	if v6 {
		if len(b) < 24 {
			return netip.Addr{}, false
		}
		return netip.AddrFrom16([16]byte(b[8:24])), true
	}
	if len(b) < 8 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte(b[4:8])), true
}
