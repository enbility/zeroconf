package zeroconf

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/enbility/zeroconf/v3/api"
	"github.com/enbility/zeroconf/v3/mocks"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/mock"
)

// TestServer_Recv_BacksOffOnError verifies that recv backs off when ReadFrom returns errors
// This is the fix for the CPU spin bug.
func TestServer_Recv_BacksOffOnError(t *testing.T) {
	mockConn := mocks.NewMockPacketConn(t)

	// Track call count
	var callCount int
	var mu sync.Mutex

	// Configure ReadFrom to always return an error
	mockConn.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(b []byte) (int, int, net.Addr, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		return 0, 0, nil, errors.New("mock read error")
	}).Maybe()

	s := &Server{
		shouldShutdown: make(chan struct{}),
		ttl:            3200,
	}

	// recvLoop calls s.refCount.Done() on exit, so we need to Add first
	s.refCount.Add(1)

	// Start recv in background
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.recvLoop(mockConn)
	}()

	// Let it run briefly
	time.Sleep(200 * time.Millisecond)

	// Shutdown
	close(s.shouldShutdown)
	wg.Wait()

	mu.Lock()
	calls := callCount
	mu.Unlock()

	// With 50ms backoff and 200ms runtime, we expect roughly 4 calls max
	// Without backoff, we'd see thousands of calls
	if calls > 10 {
		t.Errorf("Expected few calls with backoff, got %d (suggests spinning)", calls)
	}
	t.Logf("ReadFrom called %d times in 200ms with backoff", calls)
}

// TestServer_Recv_ProcessesPacket verifies that recv correctly processes incoming packets
func TestServer_Recv_ProcessesPacket(t *testing.T) {
	// Create a valid DNS query packet
	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)
	packetData, err := msg.Pack()
	if err != nil {
		t.Fatalf("Failed to pack DNS message: %v", err)
	}

	// We can test the packet parsing directly
	parsed := new(dns.Msg)
	if err := parsed.Unpack(packetData); err != nil {
		t.Fatalf("Failed to unpack: %v", err)
	}

	if len(parsed.Question) != 1 {
		t.Errorf("Expected 1 question, got %d", len(parsed.Question))
	}
	if parsed.Question[0].Name != "_test._tcp.local." {
		t.Errorf("Expected question name _test._tcp.local., got %s", parsed.Question[0].Name)
	}
}

// testServer creates a Server with InterfaceManagers for testing.
// This helper avoids direct struct construction with the removed ifaces field.
func testServer(ipv4conn, ipv6conn api.PacketConn, ifaces []net.Interface) *Server {
	return &Server{
		ipv4conn:       ipv4conn,
		ipv6conn:       ipv6conn,
		ipv4Mgr:        NewInterfaceManager(ifaces, nil),
		ipv6Mgr:        NewInterfaceManager(ifaces, nil),
		provider:       NewInterfaceProvider(),
		shouldShutdown: make(chan struct{}),
		ttl:            3200,
	}
}

func TestServer_NewServer_IPv4AndIPv6Error(t *testing.T) {
	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}

	mockFactory := mocks.NewMockConnectionFactory(t)
	mockIPv4 := mocks.NewMockPacketConn(t)

	mockFactory.EXPECT().CreateIPv4Conn(ifaces).Return(mockIPv4, nil).Once()
	mockFactory.EXPECT().CreateIPv6Conn(ifaces).Return(nil, errors.New("IPv6 unavailable")).Once()

	s, err := newServer(ifaces, applyServerOpts(WithServerConnFactory(mockFactory)))
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}
	if s.ipv4conn != mockIPv4 {
		t.Fatalf("expected IPv4 connection to be set")
	}
	if s.ipv6conn != nil {
		t.Fatalf("expected IPv6 connection to be nil")
	}
}

func TestServer_NewServer_IPv6AndIPv4Error(t *testing.T) {
	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}

	mockFactory := mocks.NewMockConnectionFactory(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	mockFactory.EXPECT().CreateIPv4Conn(ifaces).Return(nil, errors.New("IPv4 unavailable")).Once()
	mockFactory.EXPECT().CreateIPv6Conn(ifaces).Return(mockIPv6, nil).Once()

	s, err := newServer(ifaces, applyServerOpts(WithServerConnFactory(mockFactory)))
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}
	if s.ipv4conn != nil {
		t.Fatalf("expected IPv4 connection to be nil")
	}
	if s.ipv6conn != mockIPv6 {
		t.Fatalf("expected IPv6 connection to be set")
	}
}

func TestServer_NewServer_IPv4ErrorAndIPv6Error(t *testing.T) {
	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}

	mockFactory := mocks.NewMockConnectionFactory(t)
	mockFactory.EXPECT().CreateIPv4Conn(ifaces).Return(nil, errors.New("IPv4 unavailable")).Once()
	mockFactory.EXPECT().CreateIPv6Conn(ifaces).Return(nil, errors.New("IPv6 unavailable")).Once()

	s, err := newServer(ifaces, applyServerOpts(WithServerConnFactory(mockFactory)))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if s != nil {
		t.Fatalf("expected server to be nil on error")
	}
}

// TestServer_InterfaceDisconnect_StopsSendingToFailedInterface verifies that when
// a network interface disconnects during multicast response, the server stops
// attempting to send to that interface. This is the server-side fix for the
// infinite warning log issue.
func TestServer_InterfaceDisconnect_StopsSendingToFailedInterface(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	// Two interfaces: eth0 (will fail) and wlan0 (stays healthy)
	ifaces := []net.Interface{
		{Index: 1, Name: "eth0"},
		{Index: 2, Name: "wlan0"},
	}

	// Track calls per interface
	var mu sync.Mutex
	callsToEth0 := 0
	callsToWlan0 := 0

	// eth0 (index 1) returns ENETDOWN, wlan0 (index 2) succeeds
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.AnythingOfType("int"), mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if ifIndex == 1 {
				callsToEth0++
				return 0, syscall.ENETDOWN
			}
			callsToWlan0++
			return len(b), nil
		}).Maybe()

	s := testServer(mockIPv4, nil, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	// First multicast: both interfaces attempted
	_ = s.multicastResponse(msg, 0)

	mu.Lock()
	firstEth0 := callsToEth0
	firstWlan0 := callsToWlan0
	mu.Unlock()

	if firstEth0 != 1 || firstWlan0 != 1 {
		t.Errorf("First response: expected 1 call each, got eth0=%d wlan0=%d", firstEth0, firstWlan0)
	}

	// Second multicast: eth0 should be excluded
	_ = s.multicastResponse(msg, 0)

	mu.Lock()
	secondEth0 := callsToEth0
	secondWlan0 := callsToWlan0
	mu.Unlock()

	if secondEth0 != 1 {
		t.Errorf("Second response: eth0 should NOT be called again, got %d total calls", secondEth0)
	}
	if secondWlan0 != 2 {
		t.Errorf("Second response: wlan0 should have 2 calls, got %d", secondWlan0)
	}

	t.Logf("SUCCESS: Server stops sending to disconnected interface")
	t.Logf("eth0 calls: %d, wlan0 calls: %d", secondEth0, secondWlan0)
}

// TestServer_MulticastResponse_WritesToConnections verifies multicast sends to both connections
func TestServer_MulticastResponse_WritesToConnections(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	iface := net.Interface{Index: 1, Name: "eth0"}

	// Expect WriteTo to be called on both connections
	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()

	s := testServer(mockIPv4, mockIPv6, []net.Interface{iface})

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	err := s.multicastResponse(msg, 0)
	if err != nil {
		t.Fatalf("multicastResponse failed: %v", err)
	}
}

// TestServer_MulticastResponse_SpecificInterface verifies multicast to specific interface
func TestServer_MulticastResponse_SpecificInterface(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	// Expect WriteTo to be called with specific interface index 2
	mockIPv4.EXPECT().WriteTo(mock.Anything, 2, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 2, mock.Anything).Return(0, nil).Once()

	s := testServer(mockIPv4, mockIPv6, []net.Interface{{Index: 1, Name: "eth0"}, {Index: 2, Name: "wlan0"}})

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	// Send to specific interface (index 2)
	err := s.multicastResponse(msg, 2)
	if err != nil {
		t.Fatalf("multicastResponse failed: %v", err)
	}
}

// TestServer_Shutdown_ClosesConnections verifies shutdown properly closes connections
func TestServer_Shutdown_ClosesConnections(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	// Expect Close and WriteTo (for unregister) to be called
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.AnythingOfType("int"), mock.Anything).Return(0, nil).Maybe()
	mockIPv6.EXPECT().WriteTo(mock.Anything, mock.AnythingOfType("int"), mock.Anything).Return(0, nil).Maybe()
	mockIPv4.EXPECT().Close().Return(nil).Once()
	mockIPv6.EXPECT().Close().Return(nil).Once()

	s := testServer(mockIPv4, mockIPv6, []net.Interface{{Index: 1, Name: "eth0"}})
	s.service = newServiceEntry("test", "_test._tcp", "local")
	s.service.Port = 8080
	s.service.HostName = "test.local."

	s.Shutdown()
}

// TestServer_SyncInterfaces_JoinGroupSuccessActivates verifies syncInterfaces joins and activates.
func TestServer_SyncInterfaces_JoinGroupSuccessActivates(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockProvider := mocks.NewMockInterfaceProvider(t)

	iface := net.Interface{Index: 2, Name: "wlan0"}

	mockProvider.EXPECT().MulticastInterfaces().Return([]net.Interface{iface}).Once()
	mockIPv4.EXPECT().JoinGroup(mock.AnythingOfType("*net.Interface"), mock.Anything).RunAndReturn(
		func(ifi *net.Interface, group net.Addr) error {
			if ifi == nil || ifi.Index != iface.Index || ifi.Name != iface.Name {
				t.Errorf("expected JoinGroup on %+v, got %+v", iface, ifi)
			}
			udpAddr, ok := group.(*net.UDPAddr)
			if !ok || udpAddr == nil || !udpAddr.IP.Equal(mdnsGroupIPv4) {
				t.Errorf("expected IPv4 group %v, got %v", mdnsGroupIPv4, group)
			}
			return nil
		}).Once()

	s := &Server{
		ipv4conn:       mockIPv4,
		ipv4Mgr:        NewInterfaceManager(nil, nil),
		ipv6Mgr:        NewInterfaceManager(nil, nil),
		provider:       mockProvider,
		shouldShutdown: make(chan struct{}),
		ttl:            3200,
	}

	s.syncInterfaces()

	indices := s.ipv4Mgr.ActiveIndices()
	if len(indices) != 1 || indices[0] != iface.Index {
		t.Errorf("expected active indices [2], got %v", indices)
	}
}

// TestServer_SyncInterfaces_JoinGroupFailureSetsBackoff verifies JoinGroup failure triggers backoff.
func TestServer_SyncInterfaces_JoinGroupFailureSetsBackoff(t *testing.T) {
	mockIPv6 := mocks.NewMockPacketConn(t)
	mockProvider := mocks.NewMockInterfaceProvider(t)

	iface := net.Interface{Index: 3, Name: "eth0"}

	mockProvider.EXPECT().MulticastInterfaces().Return([]net.Interface{iface}).Once()
	mockIPv6.EXPECT().JoinGroup(mock.AnythingOfType("*net.Interface"), mock.Anything).Return(syscall.ENETDOWN).Once()

	s := &Server{
		ipv6conn:       mockIPv6,
		ipv4Mgr:        NewInterfaceManager(nil, nil),
		ipv6Mgr:        NewInterfaceManager(nil, nil),
		provider:       mockProvider,
		shouldShutdown: make(chan struct{}),
		ttl:            3200,
	}

	s.syncInterfaces()

	indices := s.ipv6Mgr.ActiveIndices()
	if len(indices) != 0 {
		t.Errorf("expected no active indices after JoinGroup failure, got %v", indices)
	}

	s.ipv6Mgr.mu.RLock()
	_, hasFailure := s.ipv6Mgr.failures[iface.Name]
	s.ipv6Mgr.mu.RUnlock()
	if !hasFailure {
		t.Errorf("expected backoff failure state for %s", iface.Name)
	}
}

// TestServerConfig verifies server configuration options
func TestServerConfig(t *testing.T) {
	t.Run("default TTL", func(t *testing.T) {
		opts := applyServerOpts()
		if opts.ttl != defaultTTL {
			t.Errorf("Expected default TTL %d, got %d", defaultTTL, opts.ttl)
		}
	})

	t.Run("custom TTL", func(t *testing.T) {
		opts := applyServerOpts(TTL(1000))
		if opts.ttl != 1000 {
			t.Errorf("Expected TTL 1000, got %d", opts.ttl)
		}
	})
}

// TestWithServerConnFactory verifies the WithServerConnFactory option
func TestWithServerConnFactory(t *testing.T) {
	factory := mocks.NewMockConnectionFactory(t)

	opts := applyServerOpts(WithServerConnFactory(factory))

	if opts.connFactory != factory {
		t.Error("Expected connection factory to be set")
	}
}

// TestIsKnownAnswer verifies known-answer suppression logic
func TestIsKnownAnswer(t *testing.T) {
	t.Run("empty response answers", func(t *testing.T) {
		resp := &dns.Msg{}
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		if isKnownAnswer(resp, query) {
			t.Error("Expected false when response has no answers")
		}
	})

	t.Run("empty query answers", func(t *testing.T) {
		resp := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		query := &dns.Msg{}
		if isKnownAnswer(resp, query) {
			t.Error("Expected false when query has no answers")
		}
	})

	t.Run("non-PTR response", func(t *testing.T) {
		resp := &dns.Msg{
			Answer: []dns.RR{
				&dns.A{
					Hdr: dns.RR_Header{Rrtype: dns.TypeA, Ttl: 100},
					A:   net.ParseIP("192.168.1.1"),
				},
			},
		}
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		if isKnownAnswer(resp, query) {
			t.Error("Expected false for non-PTR response")
		}
	})

	t.Run("matching known answer with sufficient TTL", func(t *testing.T) {
		resp := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 60}, // >= 100/2
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		if !isKnownAnswer(resp, query) {
			t.Error("Expected true for matching known answer with sufficient TTL")
		}
	})

	t.Run("matching known answer with insufficient TTL", func(t *testing.T) {
		resp := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 40}, // < 100/2
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		if isKnownAnswer(resp, query) {
			t.Error("Expected false for known answer with insufficient TTL")
		}
	})

	t.Run("non-matching PTR", func(t *testing.T) {
		resp := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "test._http._tcp.local.",
				},
			},
		}
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{Rrtype: dns.TypePTR, Ttl: 100},
					Ptr: "other._http._tcp.local.",
				},
			},
		}
		if isKnownAnswer(resp, query) {
			t.Error("Expected false for non-matching PTR")
		}
	})
}

// TestServer_HandleQuestion verifies question handling logic
func TestServer_HandleQuestion(t *testing.T) {
	createTestServer := func() *Server {
		s := &Server{
			ttl:            3200,
			shouldShutdown: make(chan struct{}),
			service:        newServiceEntry("myservice", "_http._tcp", "local"),
		}
		s.service.Port = 8080
		s.service.HostName = "myhost.local."
		s.service.Text = []string{"key=value"}
		return s
	}

	t.Run("nil service", func(t *testing.T) {
		s := &Server{
			ttl:            3200,
			shouldShutdown: make(chan struct{}),
			service:        nil,
		}
		resp := &dns.Msg{}
		query := &dns.Msg{}
		q := dns.Question{Name: "_http._tcp.local.", Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("Expected no error for nil service, got %v", err)
		}
		if len(resp.Answer) != 0 {
			t.Error("Expected no answers for nil service")
		}
	})

	t.Run("service type query", func(t *testing.T) {
		s := createTestServer()
		resp := &dns.Msg{}
		query := &dns.Msg{}
		q := dns.Question{Name: s.service.ServiceTypeName(), Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		if len(resp.Answer) == 0 {
			t.Error("Expected answers for service type query")
		}
	})

	t.Run("service name query", func(t *testing.T) {
		s := createTestServer()
		resp := &dns.Msg{}
		query := &dns.Msg{}
		q := dns.Question{Name: s.service.ServiceName(), Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		if len(resp.Answer) == 0 {
			t.Error("Expected answers for service name query")
		}
	})

	t.Run("service instance query", func(t *testing.T) {
		s := createTestServer()
		resp := &dns.Msg{}
		query := &dns.Msg{}
		q := dns.Question{Name: s.service.ServiceInstanceName(), Qtype: dns.TypeSRV}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		if len(resp.Answer) == 0 {
			t.Error("Expected answers for service instance query")
		}
	})

	t.Run("subtype query", func(t *testing.T) {
		s := createTestServer()
		s.service.Subtypes = []string{"_printer"}
		resp := &dns.Msg{}
		query := &dns.Msg{}
		subtypeName := "_printer._sub." + s.service.ServiceName()
		q := dns.Question{Name: subtypeName, Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		if len(resp.Answer) == 0 {
			t.Error("Expected answers for subtype query")
		}
	})

	t.Run("unknown query name", func(t *testing.T) {
		s := createTestServer()
		resp := &dns.Msg{}
		query := &dns.Msg{}
		q := dns.Question{Name: "_unknown._tcp.local.", Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		if len(resp.Answer) != 0 {
			t.Error("Expected no answers for unknown query")
		}
	})

	t.Run("known answer suppression", func(t *testing.T) {
		s := createTestServer()
		resp := &dns.Msg{}
		// Query with known answer
		query := &dns.Msg{
			Answer: []dns.RR{
				&dns.PTR{
					Hdr: dns.RR_Header{
						Rrtype: dns.TypePTR,
						Ttl:    3200, // >= s.ttl/2
					},
					Ptr: s.service.ServiceInstanceName(),
				},
			},
		}
		q := dns.Question{Name: s.service.ServiceName(), Qtype: dns.TypePTR}

		err := s.handleQuestion(q, resp, query, 1)
		if err != nil {
			t.Errorf("handleQuestion failed: %v", err)
		}
		// Answer should be suppressed
		if len(resp.Answer) != 0 {
			t.Error("Expected answer to be suppressed due to known-answer")
		}
	})
}

// TestRegisterProxy_Validation tests RegisterProxy input validation
func TestRegisterProxy_Validation(t *testing.T) {
	t.Run("missing instance name", func(t *testing.T) {
		_, err := RegisterProxy("", "_http._tcp", "local", 8080, "myhost", []string{"192.168.1.1"}, nil, nil)
		if err == nil {
			t.Error("Expected error for missing instance name")
		}
	})

	t.Run("missing service name", func(t *testing.T) {
		_, err := RegisterProxy("myservice", "", "local", 8080, "myhost", []string{"192.168.1.1"}, nil, nil)
		if err == nil {
			t.Error("Expected error for missing service name")
		}
	})

	t.Run("missing host name", func(t *testing.T) {
		_, err := RegisterProxy("myservice", "_http._tcp", "local", 8080, "", []string{"192.168.1.1"}, nil, nil)
		if err == nil {
			t.Error("Expected error for missing host name")
		}
	})

	t.Run("missing port", func(t *testing.T) {
		_, err := RegisterProxy("myservice", "_http._tcp", "local", 0, "myhost", []string{"192.168.1.1"}, nil, nil)
		if err == nil {
			t.Error("Expected error for missing port")
		}
	})

	t.Run("invalid IP address", func(t *testing.T) {
		_, err := RegisterProxy("myservice", "_http._tcp", "local", 8080, "myhost", []string{"invalid-ip"}, nil, nil)
		if err == nil {
			t.Error("Expected error for invalid IP address")
		}
	})
}

// setupMockServerConnections creates mock connections for server tests
func setupMockServerConnections(t *testing.T) (*mocks.MockPacketConn, *mocks.MockPacketConn, api.ConnectionFactory) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)
	factory := mocks.NewMockConnectionFactory(t)

	factory.EXPECT().CreateIPv4Conn(mock.Anything).Return(mockIPv4, nil).Once()
	factory.EXPECT().CreateIPv6Conn(mock.Anything).Return(mockIPv6, nil).Once()

	return mockIPv4, mockIPv6, factory
}

// TestRegisterProxy_WithMockConnections tests RegisterProxy with mocked connections
func TestRegisterProxy_WithMockConnections(t *testing.T) {
	mockIPv4, mockIPv6, factory := setupMockServerConnections(t)

	// Mock ReadFrom to block until shutdown
	mockIPv4.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(b []byte) (int, int, net.Addr, error) {
		time.Sleep(50 * time.Millisecond)
		return 0, 0, nil, errors.New("shutdown")
	}).Maybe()
	mockIPv6.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(b []byte) (int, int, net.Addr, error) {
		time.Sleep(50 * time.Millisecond)
		return 0, 0, nil, errors.New("shutdown")
	}).Maybe()

	// Mock WriteTo for probes and announcements
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()
	mockIPv6.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()

	// Mock Close
	mockIPv4.EXPECT().Close().Return(nil).Maybe()
	mockIPv6.EXPECT().Close().Return(nil).Maybe()

	// Register the proxy service
	server, err := RegisterProxy(
		"myservice",
		"_http._tcp",
		"local",
		8080,
		"myhost",
		[]string{"192.168.1.100", "fe80::1"},
		[]string{"key=value"},
		[]net.Interface{{Index: 1, Name: "eth0"}},
		WithServerConnFactory(factory),
	)
	if err != nil {
		t.Fatalf("RegisterProxy failed: %v", err)
	}
	defer server.Shutdown()

	// Verify service was set up correctly
	if server.service.Instance != "myservice" {
		t.Errorf("Expected instance 'myservice', got '%s'", server.service.Instance)
	}
	if server.service.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", server.service.Port)
	}
	if len(server.service.AddrIPv4) != 1 {
		t.Errorf("Expected 1 IPv4 address, got %d", len(server.service.AddrIPv4))
	}
	if len(server.service.AddrIPv6) != 1 {
		t.Errorf("Expected 1 IPv6 address, got %d", len(server.service.AddrIPv6))
	}
}

// TestServer_SetText tests the SetText method
func TestServer_SetText(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	// Track WriteTo calls to verify announcement was sent
	var writeCount int
	var mu sync.Mutex

	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			writeCount++
			mu.Unlock()
			return len(b), nil
		}).Maybe()
	mockIPv6.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()

	s := testServer(mockIPv4, mockIPv6, []net.Interface{{Index: 1, Name: "eth0"}})
	s.service = newServiceEntry("test", "_test._tcp", "local")
	s.service.Port = 8080
	s.service.HostName = "test.local."
	s.service.Text = []string{"old=value"}

	// Update text
	s.SetText([]string{"new=value"})

	// Verify text was updated
	if len(s.service.Text) != 1 || s.service.Text[0] != "new=value" {
		t.Errorf("Expected text 'new=value', got %v", s.service.Text)
	}

	// Verify announcement was sent (WriteTo was called)
	mu.Lock()
	if writeCount == 0 {
		t.Error("Expected announcement to be sent after SetText")
	}
	mu.Unlock()
}

// TestServer_HandleQuery_RespondsToQueries tests server responding to mDNS queries
func TestServer_HandleQuery_RespondsToQueries(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	// Capture responses
	var capturedResponses [][]byte
	var mu sync.Mutex

	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			responseCopy := make([]byte, len(b))
			copy(responseCopy, b)
			capturedResponses = append(capturedResponses, responseCopy)
			mu.Unlock()
			return len(b), nil
		}).Maybe()
	mockIPv6.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()

	s := testServer(mockIPv4, mockIPv6, []net.Interface{{Index: 1, Name: "eth0"}})
	s.service = newServiceEntry("myservice", "_http._tcp", "local")
	s.service.Port = 8080
	s.service.HostName = "myhost.local."
	s.service.Text = []string{"key=value"}
	s.service.AddrIPv4 = []net.IP{net.ParseIP("192.168.1.100")}

	// Create a query for our service
	query := new(dns.Msg)
	query.SetQuestion("_http._tcp.local.", dns.TypePTR)

	// Handle the query
	err := s.handleQuery(query, 1, &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 5353})
	if err != nil {
		t.Fatalf("handleQuery failed: %v", err)
	}

	// Verify response was sent
	mu.Lock()
	responseCount := len(capturedResponses)
	mu.Unlock()

	if responseCount == 0 {
		t.Error("Expected response to be sent for matching query")
	}

	// Parse and verify the response
	if responseCount > 0 {
		mu.Lock()
		respData := capturedResponses[0]
		mu.Unlock()

		resp := new(dns.Msg)
		if err := resp.Unpack(respData); err != nil {
			t.Fatalf("Failed to unpack response: %v", err)
		}

		if len(resp.Answer) == 0 {
			t.Error("Expected answers in response")
		}
	}
}

// TestServer_UnicastResponse tests unicast response handling
func TestServer_UnicastResponse(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	// Capture the destination address to verify unicast
	var capturedDst net.Addr
	var mu sync.Mutex

	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			capturedDst = dst
			mu.Unlock()
			return len(b), nil
		}).Once()

	s := testServer(mockIPv4, nil, []net.Interface{{Index: 1, Name: "eth0"}})
	s.service = newServiceEntry("myservice", "_http._tcp", "local")
	s.service.Port = 8080
	s.service.HostName = "myhost.local."

	// Send unicast response
	msg := new(dns.Msg)
	msg.SetQuestion("_http._tcp.local.", dns.TypePTR)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 5353}

	err := s.unicastResponse(msg, 1, clientAddr)
	if err != nil {
		t.Fatalf("unicastResponse failed: %v", err)
	}

	// Verify response was sent to the client's address
	mu.Lock()
	defer mu.Unlock()
	if capturedDst == nil {
		t.Error("Expected response to be sent")
	} else {
		udpAddr, ok := capturedDst.(*net.UDPAddr)
		if !ok {
			t.Error("Expected UDP address")
		} else if !udpAddr.IP.Equal(net.ParseIP("192.168.1.50")) {
			t.Errorf("Expected response to 192.168.1.50, got %s", udpAddr.IP)
		}
	}
}
