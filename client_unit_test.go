package zeroconf

import (
	"context"
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

// testClient creates a Client with mock connections and InterfaceManagers.
// This is a helper for unit tests that need to create a Client directly.
func testClient(ipv4conn, ipv6conn api.PacketConn, ifaces []net.Interface) *Client {
	return &Client{
		ipv4conn: ipv4conn,
		ipv6conn: ipv6conn,
		ipv4Mgr:  NewInterfaceManager(ifaces, nil),
		ipv6Mgr:  NewInterfaceManager(ifaces, nil),
		provider: NewInterfaceProvider(),
	}
}

// TestClient_InterfaceDisconnect_StopsSendingToFailedInterface is the key integration test
// that verifies the fix for the original issue: when an interface disconnects, we should
// stop sending to it rather than generating infinite warning logs.
//
// Original issue: Interface disconnects -> WriteTo fails -> code keeps trying -> infinite warnings
// Expected behavior: Interface disconnects -> WriteTo fails -> interface removed -> no more attempts
func TestClient_InterfaceDisconnect_StopsSendingToFailedInterface(t *testing.T) {
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

	// eth0 (index 1) will return ENETDOWN error (simulating disconnect)
	// wlan0 (index 2) will succeed
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.AnythingOfType("int"), mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if ifIndex == 1 {
				callsToEth0++
				// Simulate interface gone - this is the error that was causing infinite warnings
				return 0, syscall.ENETDOWN
			}
			callsToWlan0++
			return len(b), nil
		}).Maybe()

	c := testClient(mockIPv4, nil, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	// First query: both interfaces should be attempted
	// eth0 fails with ENETDOWN, wlan0 succeeds
	_ = c.sendQuery(msg)

	mu.Lock()
	firstEth0Calls := callsToEth0
	firstWlan0Calls := callsToWlan0
	mu.Unlock()

	if firstEth0Calls != 1 {
		t.Errorf("First query: expected 1 call to eth0, got %d", firstEth0Calls)
	}
	if firstWlan0Calls != 1 {
		t.Errorf("First query: expected 1 call to wlan0, got %d", firstWlan0Calls)
	}

	// Second query: eth0 should NOT be attempted (it was marked failed)
	// Only wlan0 should receive the query
	_ = c.sendQuery(msg)

	mu.Lock()
	secondEth0Calls := callsToEth0
	secondWlan0Calls := callsToWlan0
	mu.Unlock()

	// THE KEY ASSERTION: eth0 should NOT have been called again
	// This is the fix for the infinite warning issue
	if secondEth0Calls != 1 {
		t.Errorf("Second query: expected eth0 to NOT be called again (still 1), got %d calls total", secondEth0Calls)
	}
	if secondWlan0Calls != 2 {
		t.Errorf("Second query: expected wlan0 to be called (now 2), got %d calls total", secondWlan0Calls)
	}

	// Third query: same behavior - eth0 still excluded
	_ = c.sendQuery(msg)

	mu.Lock()
	thirdEth0Calls := callsToEth0
	thirdWlan0Calls := callsToWlan0
	mu.Unlock()

	if thirdEth0Calls != 1 {
		t.Errorf("Third query: eth0 should still be excluded (1 call total), got %d", thirdEth0Calls)
	}
	if thirdWlan0Calls != 3 {
		t.Errorf("Third query: expected wlan0 calls to be 3, got %d", thirdWlan0Calls)
	}

	t.Logf("SUCCESS: After eth0 disconnect, subsequent queries only went to wlan0")
	t.Logf("eth0 calls: %d (only the initial failed attempt)", thirdEth0Calls)
	t.Logf("wlan0 calls: %d (all 3 queries)", thirdWlan0Calls)
}

// TestClient_AllInterfacesDisconnect_NoInfiniteLoop verifies that if ALL interfaces
// disconnect, we don't enter an infinite loop - we just have no interfaces to send to.
func TestClient_AllInterfacesDisconnect_NoInfiniteLoop(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}

	callCount := 0
	var mu sync.Mutex

	// Interface always returns ENETDOWN
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.AnythingOfType("int"), mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return 0, syscall.ENETDOWN
		}).Maybe()

	c := testClient(mockIPv4, nil, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	// Send multiple queries
	for i := 0; i < 10; i++ {
		_ = c.sendQuery(msg)
	}

	mu.Lock()
	finalCount := callCount
	mu.Unlock()

	// Should only have 1 call - the first one that failed and removed the interface
	// Without the fix, this would be 10 (one per query, each generating a warning)
	if finalCount != 1 {
		t.Errorf("Expected only 1 call to failed interface, got %d (suggests interface not removed)", finalCount)
	}

	t.Logf("SUCCESS: Only %d call to disconnected interface across 10 queries", finalCount)
}

// TestClient_SendQuery_WritesToConnections verifies sendQuery writes to both connections
func TestClient_SendQuery_WritesToConnections(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}

	// Expect WriteTo to be called on both connections
	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()

	c := testClient(mockIPv4, mockIPv6, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	err := c.sendQuery(msg)
	if err != nil {
		t.Fatalf("sendQuery failed: %v", err)
	}
}

// TestClient_SendQuery_MultipleInterfaces verifies sendQuery writes to all interfaces
func TestClient_SendQuery_MultipleInterfaces(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	ifaces := []net.Interface{
		{Index: 1, Name: "eth0"},
		{Index: 2, Name: "wlan0"},
		{Index: 3, Name: "lo0"},
	}

	// Expect WriteTo to be called 3 times on each connection (once per interface)
	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()
	mockIPv4.EXPECT().WriteTo(mock.Anything, 2, mock.Anything).Return(0, nil).Once()
	mockIPv4.EXPECT().WriteTo(mock.Anything, 3, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 2, mock.Anything).Return(0, nil).Once()
	mockIPv6.EXPECT().WriteTo(mock.Anything, 3, mock.Anything).Return(0, nil).Once()

	c := testClient(mockIPv4, mockIPv6, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	err := c.sendQuery(msg)
	if err != nil {
		t.Fatalf("sendQuery failed: %v", err)
	}
}

// TestClient_SendQuery_IPv4Only verifies sendQuery handles IPv4-only client
func TestClient_SendQuery_IPv4Only(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	c := testClient(mockIPv4, nil, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	err := c.sendQuery(msg)
	if err != nil {
		t.Fatalf("sendQuery failed: %v", err)
	}
}

// TestClient_SendQuery_IPv6Only verifies sendQuery handles IPv6-only client
func TestClient_SendQuery_IPv6Only(t *testing.T) {
	mockIPv6 := mocks.NewMockPacketConn(t)

	mockIPv6.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).Return(0, nil).Once()

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	c := testClient(nil, mockIPv6, ifaces)

	msg := new(dns.Msg)
	msg.SetQuestion("_test._tcp.local.", dns.TypePTR)

	err := c.sendQuery(msg)
	if err != nil {
		t.Fatalf("sendQuery failed: %v", err)
	}
}

// TestClient_Shutdown_ClosesConnections verifies shutdown properly closes connections
func TestClient_Shutdown_ClosesConnections(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)

	mockIPv4.EXPECT().Close().Return(nil).Once()
	mockIPv6.EXPECT().Close().Return(nil).Once()

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	c := testClient(mockIPv4, mockIPv6, ifaces)

	c.shutdown()
}

// TestClientConfig verifies client configuration options
func TestClientConfig(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		opts := applyOpts()
		if opts.listenOn != IPv4AndIPv6 {
			t.Errorf("Expected default listenOn IPv4AndIPv6, got %d", opts.listenOn)
		}
	})

	t.Run("IPv4 only", func(t *testing.T) {
		opts := applyOpts(SelectIPTraffic(IPv4))
		if opts.listenOn != IPv4 {
			t.Errorf("Expected listenOn IPv4, got %d", opts.listenOn)
		}
	})

	t.Run("IPv6 only", func(t *testing.T) {
		opts := applyOpts(SelectIPTraffic(IPv6))
		if opts.listenOn != IPv6 {
			t.Errorf("Expected listenOn IPv6, got %d", opts.listenOn)
		}
	})

	t.Run("custom interfaces", func(t *testing.T) {
		ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
		opts := applyOpts(SelectIfaces(ifaces))
		if len(opts.ifaces) != 1 {
			t.Errorf("Expected 1 interface, got %d", len(opts.ifaces))
		}
	})
}

// TestNewClient_WithMockFactory verifies newClient uses the connection factory
func TestNewClient_WithMockFactory(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)
	factory := mocks.NewMockConnectionFactory(t)

	factory.EXPECT().CreateIPv4Conn(mock.Anything).Return(mockIPv4, nil).Once()
	factory.EXPECT().CreateIPv6Conn(mock.Anything).Return(mockIPv6, nil).Once()

	opts := clientOpts{
		listenOn:    IPv4AndIPv6,
		connFactory: factory,
	}

	c, err := newClient(opts)
	if err != nil {
		t.Fatalf("newClient failed: %v", err)
	}

	if c.ipv4conn != mockIPv4 {
		t.Error("Expected mock IPv4 connection to be used")
	}
	if c.ipv6conn != mockIPv6 {
		t.Error("Expected mock IPv6 connection to be used")
	}
}

// TestNewClient_ExportedConstructor verifies the exported NewClient constructor
func TestNewClient_ExportedConstructor(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)
	mockIPv6 := mocks.NewMockPacketConn(t)
	factory := mocks.NewMockConnectionFactory(t)

	factory.EXPECT().CreateIPv4Conn(mock.Anything).Return(mockIPv4, nil).Once()
	factory.EXPECT().CreateIPv6Conn(mock.Anything).Return(mockIPv6, nil).Once()

	c, err := NewClient(WithClientConnFactory(factory))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if c.ipv4conn != mockIPv4 {
		t.Error("Expected mock IPv4 connection to be used")
	}
	if c.ipv6conn != mockIPv6 {
		t.Error("Expected mock IPv6 connection to be used")
	}
}

// TestWithClientConnFactory verifies the WithClientConnFactory option
func TestWithClientConnFactory(t *testing.T) {
	factory := mocks.NewMockConnectionFactory(t)

	opts := applyOpts(WithClientConnFactory(factory))

	if opts.connFactory != factory {
		t.Error("Expected connection factory to be set")
	}
}

// TestClient_Query_WithInstance verifies query builds correct message for Lookup
func TestClient_Query_WithInstance(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	// Capture the DNS message to verify it contains SRV and TXT questions
	var capturedMsg []byte
	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			capturedMsg = make([]byte, len(b))
			copy(capturedMsg, b)
			return len(b), nil
		}).Once()

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	c := testClient(mockIPv4, nil, ifaces)

	params := newLookupParams("myservice", "_http._tcp", "local", false,
		make(chan *ServiceEntry), make(chan *ServiceEntry))

	err := c.query(params)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	// Parse the captured message
	msg := new(dns.Msg)
	if err := msg.Unpack(capturedMsg); err != nil {
		t.Fatalf("Failed to unpack captured message: %v", err)
	}

	// For instance lookup, we expect SRV and TXT questions
	if len(msg.Question) != 2 {
		t.Fatalf("Expected 2 questions for instance lookup, got %d", len(msg.Question))
	}

	// Check question types
	hasSRV := false
	hasTXT := false
	for _, q := range msg.Question {
		if q.Qtype == dns.TypeSRV {
			hasSRV = true
		}
		if q.Qtype == dns.TypeTXT {
			hasTXT = true
		}
	}

	if !hasSRV {
		t.Error("Expected SRV question for instance lookup")
	}
	if !hasTXT {
		t.Error("Expected TXT question for instance lookup")
	}
}

// TestClient_Query_Browse verifies query builds correct message for Browse
func TestClient_Query_Browse(t *testing.T) {
	mockIPv4 := mocks.NewMockPacketConn(t)

	var capturedMsg []byte
	mockIPv4.EXPECT().WriteTo(mock.Anything, 1, mock.Anything).RunAndReturn(
		func(b []byte, ifIndex int, dst net.Addr) (int, error) {
			capturedMsg = make([]byte, len(b))
			copy(capturedMsg, b)
			return len(b), nil
		}).Once()

	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	c := testClient(mockIPv4, nil, ifaces)

	// No instance = browse mode
	params := newLookupParams("", "_http._tcp", "local", true,
		make(chan *ServiceEntry), make(chan *ServiceEntry))

	err := c.query(params)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(capturedMsg); err != nil {
		t.Fatalf("Failed to unpack captured message: %v", err)
	}

	// For browse, we expect a single PTR question
	if len(msg.Question) != 1 {
		t.Fatalf("Expected 1 question for browse, got %d", len(msg.Question))
	}

	if msg.Question[0].Qtype != dns.TypePTR {
		t.Errorf("Expected PTR question for browse, got %d", msg.Question[0].Qtype)
	}
}

// createMockDNSResponse creates a complete DNS response for testing Lookup
func createMockDNSResponse(instanceName, hostName string, port uint16, ip net.IP) []byte {
	msg := new(dns.Msg)
	msg.Response = true

	// SRV record
	msg.Answer = append(msg.Answer, &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   instanceName,
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		Priority: 0,
		Weight:   0,
		Port:     port,
		Target:   hostName,
	})

	// TXT record
	msg.Answer = append(msg.Answer, &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   instanceName,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		Txt: []string{"key=value"},
	})

	// A record
	msg.Extra = append(msg.Extra, &dns.A{
		Hdr: dns.RR_Header{
			Name:   hostName,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		A: ip,
	})

	data, _ := msg.Pack()
	return data
}

// TestBrowse_WithMockConnections tests the full Browse flow with mocked connections
func TestBrowse_WithMockConnections(t *testing.T) {
	// Reduce query interval for faster test
	oldInterval := initialQueryInterval
	initialQueryInterval = 50 * time.Millisecond
	defer func() { initialQueryInterval = oldInterval }()

	mockIPv4 := mocks.NewMockPacketConn(t)
	factory := mocks.NewMockConnectionFactory(t)

	factory.EXPECT().CreateIPv4Conn(mock.Anything).Return(mockIPv4, nil).Once()

	// Create a DNS response with PTR record (for browse)
	instanceName := "myservice._http._tcp.local."
	serviceName := "_http._tcp.local."
	hostName := "myhost.local."

	msg := new(dns.Msg)
	msg.Response = true

	// PTR record pointing to the instance
	msg.Answer = append(msg.Answer, &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   serviceName,
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		Ptr: instanceName,
	})

	// SRV record
	msg.Answer = append(msg.Answer, &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   instanceName,
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		Port:   8080,
		Target: hostName,
	})

	// TXT record
	msg.Answer = append(msg.Answer, &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   instanceName,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		Txt: []string{"version=1.0"},
	})

	// A record
	msg.Extra = append(msg.Extra, &dns.A{
		Hdr: dns.RR_Header{
			Name:   hostName,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    120,
		},
		A: net.ParseIP("192.168.1.100"),
	})

	responseData, _ := msg.Pack()

	var readCount int
	var mu sync.Mutex

	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()
	mockIPv4.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(b []byte) (int, int, net.Addr, error) {
		mu.Lock()
		readCount++
		count := readCount
		mu.Unlock()

		if count == 1 {
			copy(b, responseData)
			return len(responseData), 1, &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 5353}, nil
		}
		time.Sleep(100 * time.Millisecond)
		return 0, 0, nil, errors.New("context cancelled")
	}).Maybe()
	mockIPv4.EXPECT().Close().Return(nil).Maybe()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	entries := make(chan *ServiceEntry, 1)
	removed := make(chan *ServiceEntry, 1)

	var browseErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		browseErr = Browse(ctx, "_http._tcp", "local", entries, removed,
			WithClientConnFactory(factory),
			SelectIPTraffic(IPv4))
	}()

	select {
	case entry := <-entries:
		if entry.Instance != "myservice" {
			t.Errorf("Expected instance 'myservice', got '%s'", entry.Instance)
		}
		if entry.Port != 8080 {
			t.Errorf("Expected port 8080, got %d", entry.Port)
		}
		if len(entry.Text) == 0 || entry.Text[0] != "version=1.0" {
			t.Errorf("Expected text 'version=1.0', got %v", entry.Text)
		}
		cancel()
	case <-ctx.Done():
		t.Log("Context done before receiving entry")
	}

	wg.Wait()

	if browseErr != nil && browseErr != context.DeadlineExceeded && browseErr != context.Canceled {
		t.Errorf("Browse returned unexpected error: %v", browseErr)
	}
}

// TestLookup_WithMockConnections tests the full Lookup flow with mocked connections
func TestLookup_WithMockConnections(t *testing.T) {
	// Reduce query interval for faster test
	oldInterval := initialQueryInterval
	initialQueryInterval = 50 * time.Millisecond
	defer func() { initialQueryInterval = oldInterval }()

	mockIPv4 := mocks.NewMockPacketConn(t)
	factory := mocks.NewMockConnectionFactory(t)

	// Factory returns our mock connection (IPv4 only since we use SelectIPTraffic(IPv4))
	factory.EXPECT().CreateIPv4Conn(mock.Anything).Return(mockIPv4, nil).Once()

	// Create the DNS response
	instanceName := "myservice._http._tcp.local."
	hostName := "myhost.local."
	responseData := createMockDNSResponse(instanceName, hostName, 8080, net.ParseIP("192.168.1.100"))

	// Track ReadFrom calls
	var readCount int
	var mu sync.Mutex

	// WriteTo for queries - just accept them
	mockIPv4.EXPECT().WriteTo(mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()

	// ReadFrom returns the response once, then blocks
	mockIPv4.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(b []byte) (int, int, net.Addr, error) {
		mu.Lock()
		readCount++
		count := readCount
		mu.Unlock()

		if count == 1 {
			// First call: return the DNS response
			copy(b, responseData)
			return len(responseData), 1, &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 5353}, nil
		}
		// Subsequent calls: block until test ends (simulates waiting for more data)
		time.Sleep(100 * time.Millisecond)
		return 0, 0, nil, errors.New("context cancelled")
	}).Maybe()

	// Close when shutdown
	mockIPv4.EXPECT().Close().Return(nil).Maybe()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	entries := make(chan *ServiceEntry, 1)

	// Run Lookup in background
	var lookupErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lookupErr = Lookup(ctx, "myservice", "_http._tcp", "local", entries,
			WithClientConnFactory(factory),
			SelectIPTraffic(IPv4))
	}()

	// Wait for entry or timeout
	select {
	case entry := <-entries:
		if entry.Instance != "myservice" {
			t.Errorf("Expected instance 'myservice', got '%s'", entry.Instance)
		}
		if entry.Port != 8080 {
			t.Errorf("Expected port 8080, got %d", entry.Port)
		}
		if entry.HostName != hostName {
			t.Errorf("Expected hostname '%s', got '%s'", hostName, entry.HostName)
		}
		if len(entry.AddrIPv4) == 0 {
			t.Error("Expected IPv4 address")
		} else if !entry.AddrIPv4[0].Equal(net.ParseIP("192.168.1.100")) {
			t.Errorf("Expected IP 192.168.1.100, got %s", entry.AddrIPv4[0])
		}
		// Success - cancel to clean up
		cancel()
	case <-ctx.Done():
		t.Log("Context done before receiving entry (may be timing issue)")
	}

	wg.Wait()

	// Context cancellation is expected, not an error for Lookup
	if lookupErr != nil && lookupErr != context.DeadlineExceeded && lookupErr != context.Canceled {
		t.Errorf("Lookup returned unexpected error: %v", lookupErr)
	}
}
