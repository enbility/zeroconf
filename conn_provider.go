package zeroconf

import (
	"net"

	"github.com/enbility/zeroconf/v3/api"
)

// defaultInterfaceProvider is the production implementation of api.InterfaceProvider.
// It lists network interfaces capable of multicast communication.
type defaultInterfaceProvider struct{}

// Compile-time interface check
var _ api.InterfaceProvider = (*defaultInterfaceProvider)(nil)

// NewInterfaceProvider creates a new default interface provider.
func NewInterfaceProvider() api.InterfaceProvider {
	return &defaultInterfaceProvider{}
}

// MulticastInterfaces returns all network interfaces that are up and support multicast.
func (p *defaultInterfaceProvider) MulticastInterfaces() []net.Interface {
	var interfaces []net.Interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		if (ifi.Flags & net.FlagUp) == 0 {
			continue
		}
		if (ifi.Flags & net.FlagMulticast) > 0 {
			interfaces = append(interfaces, ifi)
		}
	}
	return interfaces
}
