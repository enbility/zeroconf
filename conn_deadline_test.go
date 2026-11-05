package zeroconf

import (
	"net"
	"testing"
	"time"

	"github.com/enbility/zeroconf/v3/api"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// deadlineConn records the write deadline set on a real UDP socket
type deadlineConn struct {
	*net.UDPConn
	deadline time.Time
}

func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	c.deadline = t
	return c.UDPConn.SetWriteDeadline(t)
}

// every write must be bounded, a stuck socket would otherwise stall the caller forever
func TestWriteToSetsDeadline(t *testing.T) {
	for _, tc := range []struct {
		network, addr string
		wrap          func(net.PacketConn) api.PacketConn
	}{
		{"udp4", "127.0.0.1:0", func(c net.PacketConn) api.PacketConn { return newIPv4PacketConn(ipv4.NewPacketConn(c)) }},
		{"udp6", "[::1]:0", func(c net.PacketConn) api.PacketConn { return newIPv6PacketConn(ipv6.NewPacketConn(c)) }},
	} {
		t.Run(tc.network, func(t *testing.T) {
			udp, err := net.ListenPacket(tc.network, tc.addr)
			if err != nil {
				t.Skipf("%s not available: %v", tc.network, err)
			}
			t.Cleanup(func() { _ = udp.Close() })

			conn := &deadlineConn{UDPConn: udp.(*net.UDPConn)}
			start := time.Now()

			if _, err := tc.wrap(conn).WriteTo([]byte("x"), 0, udp.LocalAddr()); err != nil {
				t.Fatalf("write: %v", err)
			}

			if conn.deadline.Before(start) || conn.deadline.After(time.Now().Add(writeTimeout)) {
				t.Fatalf("deadline %v not within write timeout of %v", conn.deadline, start)
			}
		})
	}
}
