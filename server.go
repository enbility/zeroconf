package zeroconf

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/enbility/zeroconf/v3/api"
	"github.com/miekg/dns"
)

const (
	// Number of Multicast responses sent for a query message (default: 1 < x < 9)
	multicastRepetitions = 2
)

var defaultTTL uint32 = 3200

type serverOpts struct {
	ttl         uint32
	connFactory api.ConnectionFactory
	provider    api.InterfaceProvider
}

func applyServerOpts(options ...ServerOption) serverOpts {
	// Apply default configuration and load supplied options.
	var conf = serverOpts{
		ttl: defaultTTL,
	}
	for _, o := range options {
		if o != nil {
			o(&conf)
		}
	}
	return conf
}

// ServerOption fills the option struct.
type ServerOption func(*serverOpts)

// TTL sets the TTL for DNS replies.
func TTL(ttl uint32) ServerOption {
	return func(o *serverOpts) {
		o.ttl = ttl
	}
}

// WithServerConnFactory sets a custom connection factory for the server.
// This is primarily useful for testing with mock connections.
func WithServerConnFactory(factory api.ConnectionFactory) ServerOption {
	return func(o *serverOpts) {
		o.connFactory = factory
	}
}

// WithServerInterfaceProvider sets a custom interface provider for the server.
// This is primarily useful for testing with mock interface lists.
func WithServerInterfaceProvider(provider api.InterfaceProvider) ServerOption {
	return func(o *serverOpts) {
		o.provider = provider
	}
}

// Register a service by given arguments. This call will take the system's hostname
// and lookup IP by that hostname.
func Register(instance, service, domain string, port int, text []string, ifaces []net.Interface, opts ...ServerOption) (*Server, error) {
	entry := newServiceEntry(instance, service, domain)
	entry.Port = port
	entry.Text = text

	if entry.Instance == "" {
		return nil, fmt.Errorf("missing service instance name")
	}
	if entry.Service == "" {
		return nil, fmt.Errorf("missing service name")
	}
	if entry.Domain == "" {
		entry.Domain = "local."
	}
	if entry.Port == 0 {
		return nil, fmt.Errorf("missing port")
	}

	var err error
	if entry.HostName == "" {
		entry.HostName, err = os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("could not determine host")
		}
	}

	if !strings.HasSuffix(trimDot(entry.HostName), entry.Domain) {
		entry.HostName = fmt.Sprintf("%s.%s.", trimDot(entry.HostName), trimDot(entry.Domain))
	}

	if len(ifaces) == 0 {
		ifaces = NewInterfaceProvider().MulticastInterfaces()
	}

	for _, iface := range ifaces {
		v4, v6 := addrsForInterface(&iface)
		entry.AddrIPv4 = append(entry.AddrIPv4, v4...)
		entry.AddrIPv6 = append(entry.AddrIPv6, v6...)
	}

	if entry.AddrIPv4 == nil && entry.AddrIPv6 == nil {
		return nil, fmt.Errorf("could not determine host IP addresses")
	}

	s, err := newServer(ifaces, applyServerOpts(opts...))
	if err != nil {
		return nil, err
	}

	s.service = entry
	s.start()

	return s, nil
}

// RegisterProxy registers a service proxy. This call will skip the hostname/IP lookup and
// will use the provided values.
func RegisterProxy(instance, service, domain string, port int, host string, ips []string, text []string, ifaces []net.Interface, opts ...ServerOption) (*Server, error) {
	entry := newServiceEntry(instance, service, domain)
	entry.Port = port
	entry.Text = text
	entry.HostName = host

	if entry.Instance == "" {
		return nil, fmt.Errorf("missing service instance name")
	}
	if entry.Service == "" {
		return nil, fmt.Errorf("missing service name")
	}
	if entry.HostName == "" {
		return nil, fmt.Errorf("missing host name")
	}
	if entry.Domain == "" {
		entry.Domain = "local"
	}
	if entry.Port == 0 {
		return nil, fmt.Errorf("missing port")
	}

	if !strings.HasSuffix(trimDot(entry.HostName), entry.Domain) {
		entry.HostName = fmt.Sprintf("%s.%s.", trimDot(entry.HostName), trimDot(entry.Domain))
	}

	for _, ip := range ips {
		ipAddr := net.ParseIP(ip)
		if ipAddr == nil {
			return nil, fmt.Errorf("failed to parse given IP: %v", ip)
		} else if ipv4 := ipAddr.To4(); ipv4 != nil {
			entry.AddrIPv4 = append(entry.AddrIPv4, ipAddr)
		} else if ipv6 := ipAddr.To16(); ipv6 != nil {
			entry.AddrIPv6 = append(entry.AddrIPv6, ipAddr)
		} else {
			return nil, fmt.Errorf("the IP is neither IPv4 nor IPv6: %#v", ipAddr)
		}
	}

	if len(ifaces) == 0 {
		ifaces = NewInterfaceProvider().MulticastInterfaces()
	}

	s, err := newServer(ifaces, applyServerOpts(opts...))
	if err != nil {
		return nil, err
	}

	s.service = entry
	s.start()

	return s, nil
}

const (
	qClassCacheFlush uint16 = 1 << 15
)

// Server structure encapsulates both IPv4/IPv6 UDP connections
type Server struct {
	service  *ServiceEntry
	ipv4conn api.PacketConn
	ipv6conn api.PacketConn
	ipv4Mgr  *InterfaceManager
	ipv6Mgr  *InterfaceManager
	provider api.InterfaceProvider

	shouldShutdown chan struct{}
	shutdownLock   sync.Mutex
	refCount       sync.WaitGroup
	isShutdown     bool
	ttl            uint32
}

// Constructs server structure
func newServer(ifaces []net.Interface, opts serverOpts) (*Server, error) {
	// Get interface provider (use default if not injected for testing)
	provider := opts.provider
	if provider == nil {
		provider = NewInterfaceProvider()
	}

	factory := opts.connFactory
	if factory == nil {
		factory = NewConnectionFactory()
	}

	// Determine mode
	var requested []string
	if len(ifaces) > 0 {
		requested = make([]string, len(ifaces))
		for i, iface := range ifaces {
			requested[i] = iface.Name
		}
	} else {
		ifaces = provider.MulticastInterfaces()
	}

	// Create SEPARATE managers for IPv4 and IPv6.
	ipv4Mgr := NewInterfaceManager(ifaces, requested)
	ipv6Mgr := NewInterfaceManager(ifaces, requested)

	ipv4conn, err4 := factory.CreateIPv4Conn(ifaces)
	if err4 != nil {
		log.Printf("[zeroconf] no suitable IPv4 interface: %s", err4.Error())
	}
	ipv6conn, err6 := factory.CreateIPv6Conn(ifaces)
	if err6 != nil {
		log.Printf("[zeroconf] no suitable IPv6 interface: %s", err6.Error())
	}
	if err4 != nil && err6 != nil {
		// No supported interface left.
		return nil, fmt.Errorf("no supported interface")
	}

	s := &Server{
		ipv4conn:       ipv4conn,
		ipv6conn:       ipv6conn,
		ipv4Mgr:        ipv4Mgr,
		ipv6Mgr:        ipv6Mgr,
		provider:       provider,
		ttl:            opts.ttl,
		shouldShutdown: make(chan struct{}),
	}

	return s, nil
}

func (s *Server) start() {
	if s.ipv4conn != nil {
		s.refCount.Add(1)
		go s.recvLoop(s.ipv4conn)
	}
	if s.ipv6conn != nil {
		s.refCount.Add(1)
		go s.recvLoop(s.ipv6conn)
	}
	s.refCount.Add(1)
	go s.probe()

	// Start interface sync goroutine
	s.refCount.Add(1)
	go s.runInterfaceSync()
}

// SetText updates and announces the TXT records
func (s *Server) SetText(text []string) {
	s.service.Text = text
	s.announceText()
}

// Shutdown closes all udp connections and unregisters the service
func (s *Server) Shutdown() {
	s.shutdownLock.Lock()
	defer s.shutdownLock.Unlock()
	if s.isShutdown {
		return
	}

	if err := s.unregister(); err != nil {
		log.Printf("failed to unregister: %s", err)
	}

	close(s.shouldShutdown)

	if s.ipv4conn != nil {
		s.ipv4conn.Close()
	}
	if s.ipv6conn != nil {
		s.ipv6conn.Close()
	}

	// Wait for connection and routines to be closed
	s.refCount.Wait()
	s.isShutdown = true
}

// recvLoop is a long running routine to receive packets from a connection.
// It uses the PacketConn interface, allowing for mock injection in tests.
func (s *Server) recvLoop(c api.PacketConn) {
	defer s.refCount.Done()
	if c == nil {
		return
	}
	buf := make([]byte, 65536)
	for {
		select {
		case <-s.shouldShutdown:
			return
		default:
			n, ifIndex, from, err := c.ReadFrom(buf)
			if err != nil {
				// Backoff to prevent CPU spin on persistent errors
				select {
				case <-s.shouldShutdown:
					return
				case <-time.After(50 * time.Millisecond):
					continue
				}
			}
			_ = s.parsePacket(buf[:n], ifIndex, from)
		}
	}
}

// parsePacket is used to parse an incoming packet
func (s *Server) parsePacket(packet []byte, ifIndex int, from net.Addr) error {
	var msg dns.Msg
	if err := msg.Unpack(packet); err != nil {
		// log.Printf("[ERR] zeroconf: Failed to unpack packet: %v", err)
		return err
	}
	return s.handleQuery(&msg, ifIndex, from)
}

// handleQuery is used to handle an incoming query
func (s *Server) handleQuery(query *dns.Msg, ifIndex int, from net.Addr) error {
	// Ignore questions with authoritative section for now
	if len(query.Ns) > 0 {
		return nil
	}

	// Handle each question
	var err error
	for _, q := range query.Question {
		resp := dns.Msg{}
		resp.SetReply(query)
		resp.Compress = true
		resp.RecursionDesired = false
		resp.Authoritative = true
		resp.Question = nil // RFC6762 section 6 "responses MUST NOT contain any questions"
		resp.Answer = []dns.RR{}
		resp.Extra = []dns.RR{}
		if err = s.handleQuestion(q, &resp, query, ifIndex); err != nil {
			// log.Printf("[ERR] zeroconf: failed to handle question %v: %v", q, err)
			continue
		}
		// Check if there is an answer
		if len(resp.Answer) == 0 {
			continue
		}

		if isUnicastQuestion(q) {
			// Send unicast
			if e := s.unicastResponse(&resp, ifIndex, from); e != nil {
				err = e
			}
		} else {
			// Send mulicast
			if e := s.multicastResponse(&resp, ifIndex); e != nil {
				err = e
			}
		}
	}

	return err
}

// RFC6762 7.1. Known-Answer Suppression
func isKnownAnswer(resp *dns.Msg, query *dns.Msg) bool {
	if len(resp.Answer) == 0 || len(query.Answer) == 0 {
		return false
	}

	if resp.Answer[0].Header().Rrtype != dns.TypePTR {
		return false
	}
	answer := resp.Answer[0].(*dns.PTR)

	for _, known := range query.Answer {
		hdr := known.Header()
		if hdr.Rrtype != answer.Hdr.Rrtype {
			continue
		}
		ptr := known.(*dns.PTR)
		if ptr.Ptr == answer.Ptr && hdr.Ttl >= answer.Hdr.Ttl/2 {
			// log.Printf("skipping known answer: %v", ptr)
			return true
		}
	}

	return false
}

// handleQuestion is used to handle an incoming question
func (s *Server) handleQuestion(q dns.Question, resp *dns.Msg, query *dns.Msg, ifIndex int) error {
	if s.service == nil {
		return nil
	}

	switch q.Name {
	case s.service.ServiceTypeName():
		s.serviceTypeName(resp, s.ttl)
		if isKnownAnswer(resp, query) {
			resp.Answer = nil
		}

	case s.service.ServiceName():
		s.composeBrowsingAnswers(resp, ifIndex)
		if isKnownAnswer(resp, query) {
			resp.Answer = nil
		}

	case s.service.ServiceInstanceName():
		s.composeLookupAnswers(resp, s.ttl, ifIndex, false)
	default:
		// handle matching subtype query
		for _, subtype := range s.service.Subtypes {
			subtype = fmt.Sprintf("%s._sub.%s", subtype, s.service.ServiceName())
			if q.Name == subtype {
				s.composeBrowsingAnswers(resp, ifIndex)
				if isKnownAnswer(resp, query) {
					resp.Answer = nil
				}
				break
			}
		}
	}

	return nil
}

func (s *Server) composeBrowsingAnswers(resp *dns.Msg, ifIndex int) {
	ptr := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Ptr: s.service.ServiceInstanceName(),
	}
	resp.Answer = append(resp.Answer, ptr)

	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}
	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	resp.Extra = append(resp.Extra, srv, txt)

	resp.Extra = s.appendAddrs(resp.Extra, s.ttl, ifIndex, false)
}

func (s *Server) composeLookupAnswers(resp *dns.Msg, ttl uint32, ifIndex int, flushCache bool) {
	// From RFC6762
	//    The most significant bit of the rrclass for a record in the Answer
	//    Section of a response message is the Multicast DNS cache-flush bit
	//    and is discussed in more detail below in Section 10.2, "Announcements
	//    to Flush Outdated Cache Entries".
	ptr := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceInstanceName(),
	}
	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    ttl,
		},
		Txt: s.service.Text,
	}
	dnssd := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceTypeName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceName(),
	}
	resp.Answer = append(resp.Answer, srv, txt, ptr, dnssd)

	for _, subtype := range s.service.Subtypes {
		resp.Answer = append(resp.Answer,
			&dns.PTR{
				Hdr: dns.RR_Header{
					Name:   subtype,
					Rrtype: dns.TypePTR,
					Class:  dns.ClassINET,
					Ttl:    ttl,
				},
				Ptr: s.service.ServiceInstanceName(),
			})
	}

	resp.Answer = s.appendAddrs(resp.Answer, ttl, ifIndex, flushCache)
}

func (s *Server) serviceTypeName(resp *dns.Msg, ttl uint32) {
	// From RFC6762
	// 9.  Service Type Enumeration
	//
	//    For this purpose, a special meta-query is defined.  A DNS query for
	//    PTR records with the name "_services._dns-sd._udp.<Domain>" yields a
	//    set of PTR records, where the rdata of each PTR record is the two-
	//    label <Service> name, plus the same domain, e.g.,
	//    "_http._tcp.<Domain>".
	dnssd := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceTypeName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceName(),
	}
	resp.Answer = append(resp.Answer, dnssd)
}

// Perform probing & announcement
// TODO: implement a proper probing & conflict resolution
func (s *Server) probe() {
	defer s.refCount.Done()

	q := new(dns.Msg)
	q.SetQuestion(s.service.ServiceInstanceName(), dns.TypePTR)
	q.RecursionDesired = false

	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}
	q.Ns = []dns.RR{srv, txt}

	// Wait for a random duration uniformly distributed between 0 and 250 ms
	// before sending the first probe packet.
	timer := time.NewTimer(time.Duration(rand.Intn(250)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.shouldShutdown:
		return
	}
	for i := 0; i < 3; i++ {
		if err := s.multicastResponse(q, 0); err != nil {
			log.Println("[ERR] zeroconf: failed to send probe:", err.Error())
		}
		timer.Reset(250 * time.Millisecond)
		select {
		case <-timer.C:
		case <-s.shouldShutdown:
			return
		}
	}

	// From RFC6762
	//    The Multicast DNS responder MUST send at least two unsolicited
	//    responses, one second apart. To provide increased robustness against
	//    packet loss, a responder MAY send up to eight unsolicited responses,
	//    provided that the interval between unsolicited responses increases by
	//    at least a factor of two with every response sent.
	timeout := time.Second
	for i := 0; i < multicastRepetitions; i++ {
		// Use active interfaces from both managers
		activeIfaces := s.ipv4Mgr.GetActiveInterfaces()
		if len(activeIfaces) == 0 {
			activeIfaces = s.ipv6Mgr.GetActiveInterfaces()
		}
		for _, intf := range activeIfaces {
			resp := new(dns.Msg)
			resp.MsgHdr.Response = true
			// TODO: make response authoritative if we are the publisher
			resp.Compress = true
			resp.Answer = []dns.RR{}
			resp.Extra = []dns.RR{}
			s.composeLookupAnswers(resp, s.ttl, intf.Index, true)
			if err := s.multicastResponse(resp, intf.Index); err != nil {
				log.Println("[ERR] zeroconf: failed to send announcement:", err.Error())
			}
		}
		timer.Reset(timeout)
		select {
		case <-timer.C:
		case <-s.shouldShutdown:
			return
		}
		timeout *= 2
	}
}

// announceText sends a Text announcement with cache flush enabled
func (s *Server) announceText() {
	resp := new(dns.Msg)
	resp.MsgHdr.Response = true

	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}

	resp.Answer = []dns.RR{txt}
	_ = s.multicastResponse(resp, 0)
}

func (s *Server) unregister() error {
	resp := new(dns.Msg)
	resp.MsgHdr.Response = true
	resp.Answer = []dns.RR{}
	resp.Extra = []dns.RR{}
	s.composeLookupAnswers(resp, 0, 0, true)
	return s.multicastResponse(resp, 0)
}

func (s *Server) appendAddrs(list []dns.RR, ttl uint32, ifIndex int, flushCache bool) []dns.RR {
	v4 := s.service.AddrIPv4
	v6 := s.service.AddrIPv6
	if len(v4) == 0 && len(v6) == 0 {
		iface, _ := net.InterfaceByIndex(ifIndex)
		if iface != nil {
			a4, a6 := addrsForInterface(iface)
			v4 = append(v4, a4...)
			v6 = append(v6, a6...)
		}
	}
	if ttl > 0 {
		// RFC6762 Section 10 says A/AAAA records SHOULD
		// use TTL of 120s, to account for network interface
		// and IP address changes.
		ttl = 120
	}
	var cacheFlushBit uint16
	if flushCache {
		cacheFlushBit = qClassCacheFlush
	}
	for _, ipv4 := range v4 {
		a := &dns.A{
			Hdr: dns.RR_Header{
				Name:   s.service.HostName,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET | cacheFlushBit,
				Ttl:    ttl,
			},
			A: ipv4,
		}
		list = append(list, a)
	}
	for _, ipv6 := range v6 {
		aaaa := &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   s.service.HostName,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET | cacheFlushBit,
				Ttl:    ttl,
			},
			AAAA: ipv6,
		}
		list = append(list, aaaa)
	}
	return list
}

func addrsForInterface(iface *net.Interface) ([]net.IP, []net.IP) {
	var v4, v6, v6local []net.IP
	addrs, _ := iface.Addrs()
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				v4 = append(v4, ipnet.IP)
			} else {
				switch ip := ipnet.IP.To16(); ip != nil {
				case ip.IsGlobalUnicast():
					v6 = append(v6, ipnet.IP)
				case ip.IsLinkLocalUnicast():
					v6local = append(v6local, ipnet.IP)
				}
			}
		}
	}
	if len(v6) == 0 {
		v6 = v6local
	}
	return v4, v6
}

// unicastResponse is used to send a unicast response packet
func (s *Server) unicastResponse(resp *dns.Msg, ifIndex int, from net.Addr) error {
	buf, err := resp.Pack()
	if err != nil {
		return err
	}
	addr := from.(*net.UDPAddr)
	if addr.IP.To4() != nil {
		_, err = s.ipv4conn.WriteTo(buf, ifIndex, addr)
		return err
	}
	_, err = s.ipv6conn.WriteTo(buf, ifIndex, addr)
	return err
}

// multicastResponse is used to send a multicast response packet
func (s *Server) multicastResponse(msg *dns.Msg, ifIndex int) error {
	buf, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("failed to pack msg %v: %w", msg, err)
	}

	// Send to IPv4 multicast group
	if s.ipv4conn != nil {
		var indices []int
		if ifIndex != 0 {
			indices = []int{ifIndex}
		} else {
			indices = s.ipv4Mgr.ActiveIndices()
		}
		for _, idx := range indices {
			if _, err := s.ipv4conn.WriteTo(buf, idx, ipv4Addr); err != nil {
				s.ipv4Mgr.MarkFailed(idx, err)
			}
		}
	}

	// Send to IPv6 multicast group
	if s.ipv6conn != nil {
		var indices []int
		if ifIndex != 0 {
			indices = []int{ifIndex}
		} else {
			indices = s.ipv6Mgr.ActiveIndices()
		}
		for _, idx := range indices {
			if _, err := s.ipv6conn.WriteTo(buf, idx, ipv6Addr); err != nil {
				s.ipv6Mgr.MarkFailed(idx, err)
			}
		}
	}

	return nil
}

func isUnicastQuestion(q dns.Question) bool {
	// From RFC6762
	// 18.12.  Repurposing of Top Bit of qclass in Question Section
	//
	//    In the Question Section of a Multicast DNS query, the top bit of the
	//    qclass field is used to indicate that unicast responses are preferred
	//    for this particular question.  (See Section 5.4.)
	return q.Qclass&qClassCacheFlush != 0
}

// runInterfaceSync periodically syncs the interface managers with the current
// system interface state, detecting recovered interfaces.
func (s *Server) runInterfaceSync() {
	defer s.refCount.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shouldShutdown:
			return
		case <-ticker.C:
			s.syncInterfaces()
		}
	}
}

// syncInterfaces updates both interface managers with current system state.
func (s *Server) syncInterfaces() {
	current := s.provider.MulticastInterfaces()
	s.ipv4Mgr.Sync(current)
	s.ipv6Mgr.Sync(current)
}
