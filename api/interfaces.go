// Package api defines the core interfaces for the zeroconf library.
// These interfaces enable dependency injection and testing.
package api

import "net"

//go:generate mockery

// PacketConn abstracts IPv4/IPv6 multicast packet connections.
// This interface unifies ipv4.PacketConn and ipv6.PacketConn by extracting
// only the IfIndex from ControlMessage, which is the only field used.
type PacketConn interface {
	// ReadFrom reads a packet from the connection.
	// Returns the number of bytes read, the interface index the packet arrived on,
	// the source address, and any error.
	ReadFrom(b []byte) (n int, ifIndex int, src net.Addr, err error)

	// WriteTo writes a packet to the destination address.
	// The ifIndex specifies which interface to send from (0 for default/all).
	WriteTo(b []byte, ifIndex int, dst net.Addr) (n int, err error)

	// Close closes the connection.
	Close() error

	// JoinGroup joins the multicast group on the specified interface.
	JoinGroup(ifi *net.Interface, group net.Addr) error

	// LeaveGroup leaves the multicast group on the specified interface.
	LeaveGroup(ifi *net.Interface, group net.Addr) error

	// SetMulticastTTL sets the TTL for outgoing multicast packets (IPv4).
	SetMulticastTTL(ttl int) error

	// SetMulticastHopLimit sets the hop limit for outgoing multicast packets (IPv6).
	SetMulticastHopLimit(hopLimit int) error

	// SetMulticastInterface sets the default interface for outgoing multicast.
	// Used as fallback on platforms where ControlMessage is not supported (Windows).
	SetMulticastInterface(ifi *net.Interface) error
}

// ConnectionFactory creates multicast connections.
// This abstraction allows injecting mock connections for testing.
type ConnectionFactory interface {
	// CreateIPv4Conn creates an IPv4 multicast connection joined to the mDNS group.
	CreateIPv4Conn(ifaces []net.Interface) (PacketConn, error)

	// CreateIPv6Conn creates an IPv6 multicast connection joined to the mDNS group.
	CreateIPv6Conn(ifaces []net.Interface) (PacketConn, error)
}

// InterfaceProvider lists network interfaces.
// This abstraction allows injecting mock interface lists for testing.
type InterfaceProvider interface {
	// MulticastInterfaces returns all network interfaces capable of multicast.
	MulticastInterfaces() []net.Interface
}
