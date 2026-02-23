package zeroconf

import (
	"log"
	"net"
	"runtime"

	"github.com/enbility/zeroconf/v3/api"
	"golang.org/x/net/ipv4"
)

// ipv4PacketConn wraps ipv4.PacketConn to implement api.PacketConn interface.
// This adapter is needed because ipv4.PacketConn uses ControlMessage for
// interface selection, but we only need the IfIndex field.
type ipv4PacketConn struct {
	conn *ipv4.PacketConn
}

// Compile-time interface check
var _ api.PacketConn = (*ipv4PacketConn)(nil)

// newIPv4PacketConn creates a new IPv4 PacketConn wrapper.
func newIPv4PacketConn(conn *ipv4.PacketConn) *ipv4PacketConn {
	return &ipv4PacketConn{conn: conn}
}

func (c *ipv4PacketConn) ReadFrom(b []byte) (n int, ifIndex int, src net.Addr, err error) {
	n, cm, src, err := c.conn.ReadFrom(b)
	if cm != nil {
		ifIndex = cm.IfIndex
	}
	return
}

func (c *ipv4PacketConn) WriteTo(b []byte, ifIndex int, dst net.Addr) (n int, err error) {
	// See https://pkg.go.dev/golang.org/x/net/ipv4#pkg-note-BUG
	// On Windows, the ControlMessage for WriteTo is not implemented.
	// Use SetMulticastInterface as fallback.
	var cm *ipv4.ControlMessage
	if ifIndex != 0 {
		switch runtime.GOOS {
		case "darwin", "ios", "linux":
			cm = &ipv4.ControlMessage{IfIndex: ifIndex}
		default:
			// Windows and other platforms: use SetMulticastInterface
			iface, _ := net.InterfaceByIndex(ifIndex)
			if iface != nil {
				if err := c.conn.SetMulticastInterface(iface); err != nil {
					log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
				}
			}
		}
	}
	return c.conn.WriteTo(b, cm, dst)
}

func (c *ipv4PacketConn) Close() error {
	return c.conn.Close()
}

func (c *ipv4PacketConn) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return c.conn.JoinGroup(ifi, group)
}

func (c *ipv4PacketConn) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return c.conn.LeaveGroup(ifi, group)
}

func (c *ipv4PacketConn) SetMulticastTTL(ttl int) error {
	return c.conn.SetMulticastTTL(ttl)
}

func (c *ipv4PacketConn) SetMulticastHopLimit(hopLimit int) error {
	// IPv4 doesn't have hop limit, this is a no-op
	return nil
}

func (c *ipv4PacketConn) SetMulticastInterface(ifi *net.Interface) error {
	return c.conn.SetMulticastInterface(ifi)
}
