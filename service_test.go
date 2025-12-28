package zeroconf

import (
	"context"
	"log"
	"runtime"
	"testing"
	"time"
)

var (
	mdnsName    = "test--xxxxxxxxxxxx"
	mdnsService = "_test--xxxx._tcp"
	mdnsSubtype = "_test--xxxx._tcp,_fancy"
	mdnsDomain  = "local."
	mdnsPort    = 8888
)

func startMDNS(t *testing.T, port int, name, service, domain string) {
	// 5353 is default mdns port
	server, err := Register(name, service, domain, port, []string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		t.Fatalf("error while registering mdns service: %s", err)
	}
	t.Cleanup(server.Shutdown)
	log.Printf("Published service: %s, type: %s, domain: %s", name, service, domain)
}

func TestQuickShutdown(t *testing.T) {
	server, err := Register(mdnsName, mdnsService, mdnsDomain, mdnsPort, []string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown took longer than 500ms")
	}
}

func TestBasic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startMDNS(t, mdnsPort, mdnsName, mdnsService, mdnsDomain)

	time.Sleep(time.Second)

	entries := make(chan *ServiceEntry, 100)
	expired := make(chan *ServiceEntry, 100)
	if err := Browse(ctx, mdnsService, mdnsDomain, entries, expired); err != nil {
		t.Fatalf("Expected browse success, but got %v", err)
	}
	<-ctx.Done()

	if len(entries) != 1 {
		t.Fatalf("Expected number of service entries is 1, but got %d", len(entries))
	}
	result := <-entries
	if result.Domain != mdnsDomain {
		t.Fatalf("Expected domain is %s, but got %s", mdnsDomain, result.Domain)
	}
	if result.Service != mdnsService {
		t.Fatalf("Expected service is %s, but got %s", mdnsService, result.Service)
	}
	if result.Instance != mdnsName {
		t.Fatalf("Expected instance is %s, but got %s", mdnsName, result.Instance)
	}
	if result.Port != mdnsPort {
		t.Fatalf("Expected port is %d, but got %d", mdnsPort, result.Port)
	}
}

func TestNoRegister(t *testing.T) {
	// before register, mdns resolve shuold not have any entry
	entries := make(chan *ServiceEntry)
	go func(results <-chan *ServiceEntry) {
		s := <-results
		if s != nil {
			t.Errorf("Expected empty service entries but got %v", *s)
		}
	}(entries)
	expired := make(chan *ServiceEntry)
	go func(results <-chan *ServiceEntry) {
		s := <-results
		if s != nil {
			t.Errorf("Expected empty service entries but got %v", *s)
		}
	}(expired)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := Browse(ctx, mdnsService, mdnsDomain, entries, expired); err != nil {
		t.Fatalf("Expected browse success, but got %v", err)
	}
	<-ctx.Done()
	cancel()
}

func TestSubtype(t *testing.T) {
	t.Run("browse with subtype", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		startMDNS(t, mdnsPort, mdnsName, mdnsSubtype, mdnsDomain)

		time.Sleep(time.Second)

		entries := make(chan *ServiceEntry, 100)
		expired := make(chan *ServiceEntry, 100)
		if err := Browse(ctx, mdnsSubtype, mdnsDomain, entries, expired); err != nil {
			t.Fatalf("Expected browse success, but got %v", err)
		}
		<-ctx.Done()

		if len(entries) != 1 {
			t.Fatalf("Expected number of service entries is 1, but got %d", len(entries))
		}
		result := <-entries
		if result.Domain != mdnsDomain {
			t.Fatalf("Expected domain is %s, but got %s", mdnsDomain, result.Domain)
		}
		if result.Service != mdnsService {
			t.Fatalf("Expected service is %s, but got %s", mdnsService, result.Service)
		}
		if result.Instance != mdnsName {
			t.Fatalf("Expected instance is %s, but got %s", mdnsName, result.Instance)
		}
		if result.Port != mdnsPort {
			t.Fatalf("Expected port is %d, but got %d", mdnsPort, result.Port)
		}
	})

	t.Run("browse without subtype", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		startMDNS(t, mdnsPort, mdnsName, mdnsSubtype, mdnsDomain)

		time.Sleep(time.Second)

		entries := make(chan *ServiceEntry, 100)
		expired := make(chan *ServiceEntry, 100)
		if err := Browse(ctx, mdnsService, mdnsDomain, entries, expired); err != nil {
			t.Fatalf("Expected browse success, but got %v", err)
		}
		<-ctx.Done()

		if len(entries) != 1 {
			t.Fatalf("Expected number of service entries is 1, but got %d", len(entries))
		}
		result := <-entries
		if result.Domain != mdnsDomain {
			t.Fatalf("Expected domain is %s, but got %s", mdnsDomain, result.Domain)
		}
		if result.Service != mdnsService {
			t.Fatalf("Expected service is %s, but got %s", mdnsService, result.Service)
		}
		if result.Instance != mdnsName {
			t.Fatalf("Expected instance is %s, but got %s", mdnsName, result.Instance)
		}
		if result.Port != mdnsPort {
			t.Fatalf("Expected port is %d, but got %d", mdnsPort, result.Port)
		}
	})

	t.Run("ttl", func(t *testing.T) {
		origTTL := defaultTTL
		origCleanupFreq := cleanupFreq
		origInitialQueryInterval := initialQueryInterval
		t.Cleanup(func() {
			defaultTTL = origTTL
			cleanupFreq = origCleanupFreq
			initialQueryInterval = origInitialQueryInterval
		})
		defaultTTL = 1 // 1 second
		initialQueryInterval = 100 * time.Millisecond
		cleanupFreq = 100 * time.Millisecond

		// Use longer timeout for CI environments where timing can be inconsistent
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		startMDNS(t, mdnsPort, mdnsName, mdnsSubtype, mdnsDomain)

		entries := make(chan *ServiceEntry, 100)
		expired := make(chan *ServiceEntry, 100)
		if err := Browse(ctx, mdnsService, mdnsDomain, entries, expired); err != nil {
			t.Fatalf("Expected browse success, but got %v", err)
		}

		<-ctx.Done()
		if len(entries) < 2 {
			t.Fatalf("Expected to have received at least 2 entries, but got %d", len(entries))
		}
		res1 := <-entries
		res2 := <-entries
		if res1.ServiceInstanceName() != res2.ServiceInstanceName() {
			t.Fatalf("expected the two entries to be identical")
		}
	})
}

// TestCPUSpinOnConnectionClose demonstrates the CPU spinning bug that occurs
// when connections are closed externally (simulating network interface changes).
// This reproduces the issue seen on Windows after hours of running.
func TestCPUSpinOnConnectionClose(t *testing.T) {
	server, err := Register(mdnsName, mdnsService, mdnsDomain, mdnsPort,
		[]string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Let the server start and stabilize
	time.Sleep(100 * time.Millisecond)

	// Measure baseline goroutine count
	baselineGoroutines := runtime.NumGoroutine()
	t.Logf("Baseline goroutines: %d", baselineGoroutines)

	// Get baseline CPU stats
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)
	baselineMallocs := baselineStats.Mallocs

	// Close connections WITHOUT triggering shutdown
	// This simulates what happens when network interfaces change on Windows
	if server.ipv4conn != nil {
		server.ipv4conn.Close()
	}
	if server.ipv6conn != nil {
		server.ipv6conn.Close()
	}

	t.Log("Connections closed - recv loops should now be spinning on errors")

	// Wait and measure - if spinning, we'll see high allocation rate
	// because the tight loop keeps running
	time.Sleep(500 * time.Millisecond)

	var afterStats runtime.MemStats
	runtime.ReadMemStats(&afterStats)
	allocsDuring := afterStats.Mallocs - baselineMallocs

	t.Logf("Allocations during 500ms after connection close: %d", allocsDuring)
	t.Logf("Current goroutines: %d", runtime.NumGoroutine())

	// A spinning loop will have many more allocations than a properly blocked one
	// This threshold is somewhat arbitrary but a blocked recv should have near-zero
	// while a spinning one will have thousands
	if allocsDuring > 10000 {
		t.Errorf("DETECTED CPU SPIN: %d allocations in 500ms indicates tight loop", allocsDuring)
		t.Log("This confirms the bug: recv loops spin when ReadFrom returns errors")
	} else {
		t.Logf("Allocation count (%d) suggests recv loops may be handling errors correctly", allocsDuring)
	}

	// Clean up - this should work even with closed connections
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Shutdown()
	}()

	select {
	case <-done:
		t.Log("Shutdown completed successfully")
	case <-time.After(2 * time.Second):
		t.Error("Shutdown timed out - recv loops may be stuck")
	}
}
