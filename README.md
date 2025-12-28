ZeroConf: Service Discovery with mDNS
=====================================
ZeroConf is a pure Golang library that employs Multicast DNS-SD for

* browsing and resolving services in your network
* registering own services

in the local network.

It basically implements aspects of the standards
[RFC 6762](https://tools.ietf.org/html/rfc6762) (mDNS) and
[RFC 6763](https://tools.ietf.org/html/rfc6763) (DNS-SD).
Though it does not support all requirements yet, the aim is to provide a compliant solution in the long-term with the community.

By now, it should be compatible to [Avahi](http://avahi.org/) (tested) and Apple's Bonjour (untested).
Target environments: private LAN/Wifi, small or isolated networks.

[![GoDoc](https://godoc.org/github.com/enbility/zeroconf?status.svg)](https://godoc.org/github.com/enbility/zeroconf)
[![Go Report Card](https://goreportcard.com/badge/github.com/enbility/zeroconf)](https://goreportcard.com/report/github.com/enbility/zeroconf)
[![Tests](https://github.com/enbility/zeroconf/actions/workflows/go-test.yml/badge.svg)](https://github.com/enbility/zeroconf/actions/workflows/go-test.yml)

## Install
Nothing is as easy as that:
```bash
$ go get -u github.com/enbility/zeroconf/v3
```

## Browse for services in your local network

```go
entries := make(chan *zeroconf.ServiceEntry)
removed := make(chan *zeroconf.ServiceEntry)

go func() {
    for {
        select {
        case entry := <-entries:
            log.Println("Found:", entry)
        case entry := <-removed:
            log.Println("Removed:", entry)
        }
    }
}()

ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
defer cancel()
// Discover all services on the network (e.g. _workstation._tcp)
err := zeroconf.Browse(ctx, "_workstation._tcp", "local.", entries, removed)
if err != nil {
    log.Fatalln("Failed to browse:", err.Error())
}

<-ctx.Done()
```
A subtype may added to service name to narrow the set of results. E.g. to browse `_workstation._tcp` with subtype `_windows`, use`_workstation._tcp,_windows`.

See https://github.com/enbility/zeroconf/blob/master/examples/resolv/client.go.

## Lookup a specific service instance

```go
entries := make(chan *zeroconf.ServiceEntry)

go func() {
    for entry := range entries {
        log.Println("Found:", entry)
    }
}()

ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
defer cancel()
// Lookup a specific service instance by name
err := zeroconf.Lookup(ctx, "MyService", "_workstation._tcp", "local.", entries)
if err != nil {
    log.Fatalln("Failed to lookup:", err.Error())
}

<-ctx.Done()
```

## Register a service

```go
server, err := zeroconf.Register("GoZeroconf", "_workstation._tcp", "local.", 42424, []string{"txtv=0", "lo=1", "la=2"}, nil)
if err != nil {
    panic(err)
}
defer server.Shutdown()

// Clean exit.
sig := make(chan os.Signal, 1)
signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
select {
case <-sig:
    // Exit by user
case <-time.After(time.Second * 120):
    // Exit by timeout
}

log.Println("Shutting down.")
```
Multiple subtypes may be added to service name, separated by commas. E.g `_workstation._tcp,_windows` has subtype `_windows`.

See https://github.com/enbility/zeroconf/blob/master/examples/register/server.go.

## Testing Support (v3)

Version 3 introduces interface-based abstractions for improved testability. You can inject mock connections for unit testing without requiring real network access:

```go
// Create mock connections using the provided interfaces
mockFactory := &MyMockConnectionFactory{}

// Client with mock connections
client, err := zeroconf.NewClient(zeroconf.WithClientConnFactory(mockFactory))

// Server with mock connections
server, err := zeroconf.RegisterProxy(
    "MyService", "_http._tcp", "local.", 8080,
    "myhost.local.", []string{"192.168.1.100"},
    []string{"txtvers=1"},
    nil, // interfaces
    zeroconf.WithServerConnFactory(mockFactory),
)
```

See the `api/` package for interface definitions and `mocks/` for mockery-generated mocks.

## Features and ToDo's
This list gives a quick impression about the state of this library.
See what needs to be done and submit a pull request :)

* [x] Browse / Lookup / Register services
* [x] Multiple IPv6 / IPv4 addresses support
* [x] Send multiple probes (exp. back-off) if no service answers (*)
* [x] Timestamp entries for TTL checks
* [x] Service removal notifications via `removed` channel
* [x] Interface-based abstractions for testability (v3)
* [ ] Compare new multicasts with already received services

_Notes:_

(*) The denoted features might not be perfectly standards compliant, but shouldn't cause any problems.
    Some tests showed improvements in overall robustness and performance with the features enabled.

## Credits
Great thanks to [hashicorp](https://github.com/hashicorp/mdns) and to [oleksandr](https://github.com/oleksandr/bonjour) and all contributing authors for the code this projects bases upon.
Large parts of the code are still the same.

However, there are several reasons why I decided to create a fork of the original project:
The previous project seems to be unmaintained. There are several useful pull requests waiting. I merged most of them in this project.
Still, the implementation has some bugs and lacks some other features that make it quite unreliable in real LAN environments when running continously.
Last but not least, the aim for this project is to build a solution that targets standard conformance in the long term with the support of the community.
Though, resiliency should remain a top goal.
