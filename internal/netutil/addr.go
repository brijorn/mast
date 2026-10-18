package netutil

import (
	"net"
	"net/netip"
)

// Tailscale hands out addresses from these two ranges and nothing else, so an
// address inside them arrived over the tailnet's WireGuard tunnel.
var tailnetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// Ranges a proxy must never dial beyond what netip already classifies: the
// shared carrier and tailnet range, "this network", IETF protocol assignments,
// benchmarking, and the reserved block that holds the broadcast address.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// RemoteAddr parses the ip:port form net/http and net.Conn report a peer in.
func RemoteAddr(hostport string) (netip.Addr, bool) {
	addrPort, err := netip.ParseAddrPort(hostport)
	if err != nil {
		addr, err := netip.ParseAddr(hostport)
		if err != nil {
			return netip.Addr{}, false
		}
		return addr.Unmap(), true
	}
	return addrPort.Addr().Unmap(), true
}

// AddrOf returns the IP a net.Addr names, for the TCP and UDP addresses a
// connection reports.
func AddrOf(addr net.Addr) (netip.Addr, bool) {
	if addr == nil {
		return netip.Addr{}, false
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		ip, ok := netip.AddrFromSlice(a.IP)
		return ip.Unmap(), ok
	case *net.UDPAddr:
		ip, ok := netip.AddrFromSlice(a.IP)
		return ip.Unmap(), ok
	}
	return RemoteAddr(addr.String())
}

func IsLoopback(addr netip.Addr) bool {
	return addr.Unmap().IsLoopback()
}

func IsTailnet(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range tailnetPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// IsPublic reports whether addr is somewhere on the internet rather than this
// machine, the local network, the tailnet, or a special-purpose range.
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
