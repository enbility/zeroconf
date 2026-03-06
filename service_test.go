package zeroconf

import (
	"context"
	"fmt"
	"log"
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

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func TestFullyQualifiedDomain(t *testing.T) {
	discoverEntry := func(t *testing.T, instance, service, domain string) *ServiceEntry {
		t.Helper()

		time.Sleep(time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		entries := make(chan *ServiceEntry, 100)
		expired := make(chan *ServiceEntry, 100)
		if err := Browse(ctx, service, fmt.Sprintf("%s.", trimDot(domain)), entries, expired); err != nil {
			t.Fatalf("Expected browse success, but got %v", err)
		}
		<-ctx.Done()

		if len(entries) == 0 {
			t.Fatal("Expected at least one service entry, but got none")
		}

		for len(entries) > 0 {
			result := <-entries
			if result.Instance == instance {
				return result
			}
		}

		t.Fatalf("Expected service entry for instance %q, but did not find it", instance)
		return nil
	}

	t.Run("Register", func(t *testing.T) {
		testCases := []struct {
			name         string
			hostName     string
			domain       string
			expectedHost string
		}{
			{
				name:         "short hostname without trailing dot in domain",
				hostName:     "Laptop-1",
				domain:       "local",
				expectedHost: "Laptop-1.local.",
			},
			{
				name:         "short hostname with trailing dot in domain",
				hostName:     "Laptop-1",
				domain:       "local.",
				expectedHost: "Laptop-1.local.",
			},
			{
				name:         "hostname including domain without trailing dot in domain",
				hostName:     "MacBook-Air.local",
				domain:       "local",
				expectedHost: "MacBook-Air.local.",
			},
			{
				name:         "hostname including domain with trailing dot in domain",
				hostName:     "MacBook-Air.local",
				domain:       "local.",
				expectedHost: "MacBook-Air.local.",
			},
		}

		for i, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				origHostnameFunc := hostnameFunc
				hostnameFunc = func() (string, error) {
					return tc.hostName, nil
				}
				t.Cleanup(func() {
					hostnameFunc = origHostnameFunc
				})

				instance := fmt.Sprintf("test-register-fqdn-%d", i)
				service := fmt.Sprintf("_fqdn-register-%d._tcp", i)

				server, err := Register(instance, service, tc.domain, mdnsPort, []string{"txtv=0"}, nil)
				if err != nil {
					t.Fatalf("error while registering mdns service: %s", err)
				}
				t.Cleanup(server.Shutdown)

				result := discoverEntry(t, instance, service, tc.domain)
				if result.Domain != "local." {
					t.Fatalf("Expected domain is local., but got %s", result.Domain)
				}
				if result.HostName != tc.expectedHost {
					t.Fatalf("Expected hostname is %s, but got %s", tc.expectedHost, result.HostName)
				}
			})
		}
	})

	t.Run("RegisterProxy", func(t *testing.T) {
		testCases := []struct {
			name         string
			hostName     string
			domain       string
			expectedHost string
		}{
			{
				name:         "short hostname without trailing dot in domain",
				hostName:     "Laptop-1",
				domain:       "local",
				expectedHost: "Laptop-1.local.",
			},
			{
				name:         "short hostname with trailing dot in domain",
				hostName:     "Laptop-1",
				domain:       "local.",
				expectedHost: "Laptop-1.local.",
			},
			{
				name:         "hostname including domain without trailing dot in domain",
				hostName:     "MacBook-Air.local",
				domain:       "local",
				expectedHost: "MacBook-Air.local.",
			},
			{
				name:         "hostname including domain with trailing dot in domain",
				hostName:     "MacBook-Air.local",
				domain:       "local.",
				expectedHost: "MacBook-Air.local.",
			},
		}

		for i, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				instance := fmt.Sprintf("test-registerproxy-fqdn-%d", i)
				service := fmt.Sprintf("_fqdn-registerproxy-%d._tcp", i)

				server, err := RegisterProxy(
					instance,
					service,
					tc.domain,
					mdnsPort,
					tc.hostName,
					[]string{"192.168.1.100"},
					[]string{"txtv=0"},
					nil,
				)
				if err != nil {
					t.Fatalf("error while registering proxy mdns service: %s", err)
				}
				t.Cleanup(server.Shutdown)

				result := discoverEntry(t, instance, service, tc.domain)
				if result.Domain != "local." {
					t.Fatalf("Expected domain is local., but got %s", result.Domain)
				}
				if result.HostName != tc.expectedHost {
					t.Fatalf("Expected hostname is %s, but got %s", tc.expectedHost, result.HostName)
				}
			})
		}
	})
}
