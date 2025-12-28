package zeroconf

import (
	"fmt"
	"net"
	"runtime"
	"syscall"

	"github.com/enbility/zeroconf/v3/api"
	"golang.org/x/net/ipv6"
)

// ipv6PacketConn wraps ipv6.PacketConn to implement api.PacketConn interface.
// This adapter is needed because ipv6.PacketConn uses ControlMessage for
// interface selection, but we only need the IfIndex field.
type ipv6PacketConn struct {
	conn *ipv6.PacketConn
}

// Compile-time interface check
var _ api.PacketConn = (*ipv6PacketConn)(nil)

// newIPv6PacketConn creates a new IPv6 PacketConn wrapper.
func newIPv6PacketConn(conn *ipv6.PacketConn) *ipv6PacketConn {
	return &ipv6PacketConn{conn: conn}
}

func (c *ipv6PacketConn) ReadFrom(b []byte) (n int, ifIndex int, src net.Addr, err error) {
	n, cm, src, err := c.conn.ReadFrom(b)
	if cm != nil {
		ifIndex = cm.IfIndex
	}
	return
}

func (c *ipv6PacketConn) WriteTo(b []byte, ifIndex int, dst net.Addr) (n int, err error) {
	// See https://pkg.go.dev/golang.org/x/net/ipv6#pkg-note-BUG
	// On Windows, the ControlMessage for WriteTo is not implemented.
	// Use SetMulticastInterface as fallback.
	var cm *ipv6.ControlMessage

	if ifIndex != 0 {
		switch runtime.GOOS {
		case "darwin", "ios", "linux":
			cm = &ipv6.ControlMessage{IfIndex: ifIndex}

		default:
			// Windows and other platforms: validate and set interface.
			// CRITICAL: Return errors instead of logging them. The caller
			// (via InterfaceManager.MarkFailed) handles removal and backoff.
			iface, err := net.InterfaceByIndex(ifIndex)
			if err != nil {
				// Interface gone - wrap with ENXIO so isInterfaceGone() detects it
				return 0, fmt.Errorf("interface index %d: %w", ifIndex, syscall.ENXIO)
			}
			// Verify interface is actually up
			if iface.Flags&net.FlagUp == 0 {
				return 0, fmt.Errorf("interface %s is down: %w", iface.Name, syscall.ENETDOWN)
			}
			if err := c.conn.SetMulticastInterface(iface); err != nil {
				// Return the actual error - may contain WSAENETDOWN or similar
				return 0, fmt.Errorf("set multicast interface %s: %w", iface.Name, err)
			}
		}
	}

	return c.conn.WriteTo(b, cm, dst)
}

func (c *ipv6PacketConn) Close() error {
	return c.conn.Close()
}

func (c *ipv6PacketConn) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return c.conn.JoinGroup(ifi, group)
}

func (c *ipv6PacketConn) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return c.conn.LeaveGroup(ifi, group)
}

func (c *ipv6PacketConn) SetMulticastTTL(ttl int) error {
	// IPv6 doesn't have TTL, this is a no-op
	return nil
}

func (c *ipv6PacketConn) SetMulticastHopLimit(hopLimit int) error {
	return c.conn.SetMulticastHopLimit(hopLimit)
}

func (c *ipv6PacketConn) SetMulticastInterface(ifi *net.Interface) error {
	return c.conn.SetMulticastInterface(ifi)
}
