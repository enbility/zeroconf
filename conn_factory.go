package zeroconf

import (
	"fmt"
	"net"

	"github.com/enbility/zeroconf/v3/api"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// defaultConnectionFactory is the production implementation of api.ConnectionFactory.
// It creates real UDP multicast connections for mDNS communication.
type defaultConnectionFactory struct{}

// Compile-time interface check
var _ api.ConnectionFactory = (*defaultConnectionFactory)(nil)

// NewConnectionFactory creates a new default connection factory.
func NewConnectionFactory() api.ConnectionFactory {
	return &defaultConnectionFactory{}
}

func (f *defaultConnectionFactory) CreateIPv4Conn(ifaces []net.Interface) (api.PacketConn, error) {
	udpConn, err := net.ListenUDP("udp4", mdnsWildcardAddrIPv4)
	if err != nil {
		return nil, err
	}

	pkConn := ipv4.NewPacketConn(udpConn)
	_ = pkConn.SetControlMessage(ipv4.FlagInterface, true)

	var failedJoins int
	for _, iface := range ifaces {
		if err := pkConn.JoinGroup(&iface, &net.UDPAddr{IP: mdnsGroupIPv4}); err != nil {
			failedJoins++
		}
	}
	if failedJoins == len(ifaces) {
		_ = pkConn.Close()
		return nil, fmt.Errorf("udp4: failed to join any of these interfaces: %v", ifaces)
	}

	_ = pkConn.SetMulticastTTL(255)

	return newIPv4PacketConn(pkConn), nil
}

func (f *defaultConnectionFactory) CreateIPv6Conn(ifaces []net.Interface) (api.PacketConn, error) {
	udpConn, err := net.ListenUDP("udp6", mdnsWildcardAddrIPv6)
	if err != nil {
		return nil, err
	}

	pkConn := ipv6.NewPacketConn(udpConn)
	_ = pkConn.SetControlMessage(ipv6.FlagInterface, true)

	var failedJoins int
	for _, iface := range ifaces {
		if err := pkConn.JoinGroup(&iface, &net.UDPAddr{IP: mdnsGroupIPv6}); err != nil {
			failedJoins++
		}
	}
	if failedJoins == len(ifaces) {
		_ = pkConn.Close()
		return nil, fmt.Errorf("udp6: failed to join any of these interfaces: %v", ifaces)
	}

	_ = pkConn.SetMulticastHopLimit(255)

	return newIPv6PacketConn(pkConn), nil
}
