package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies is the set of reverse-proxy addresses whose X-Forwarded-For
// header is believed. Requests arriving from any other peer are attributed to
// their TCP remote address and their forwarding headers are ignored.
type TrustedProxies struct {
	prefixes []netip.Prefix
}

// ParseTrustedProxies parses a comma, semicolon, or whitespace separated list
// of IP addresses and CIDR prefixes (for example
// "127.0.0.1/8,::1,172.16.0.0/12"). An empty value trusts no proxy.
func ParseTrustedProxies(raw string) (TrustedProxies, error) {
	var proxies TrustedProxies
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	for _, field := range fields {
		var prefix netip.Prefix
		if strings.Contains(field, "/") {
			parsed, err := netip.ParsePrefix(field)
			if err != nil {
				return TrustedProxies{}, fmt.Errorf("invalid trusted proxy %q: %w", field, err)
			}
			if parsed.Addr().Is4In6() {
				bits := parsed.Bits() - 96
				if bits < 0 {
					return TrustedProxies{}, fmt.Errorf("invalid trusted proxy %q: IPv4-mapped prefix is too wide", field)
				}
				parsed = netip.PrefixFrom(parsed.Addr().Unmap(), bits)
			}
			prefix = parsed.Masked()
		} else {
			addr, err := netip.ParseAddr(field)
			if err != nil {
				return TrustedProxies{}, fmt.Errorf("invalid trusted proxy %q: %w", field, err)
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		proxies.prefixes = append(proxies.prefixes, prefix)
	}
	return proxies, nil
}

// Empty reports whether no proxy is trusted.
func (p TrustedProxies) Empty() bool { return len(p.prefixes) == 0 }

func (p TrustedProxies) String() string {
	values := make([]string, 0, len(p.prefixes))
	for _, prefix := range p.prefixes {
		values = append(values, prefix.String())
	}
	return strings.Join(values, ",")
}

func (p TrustedProxies) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Option customizes the HTTP API.
type Option func(*API)

// WithTrustedProxies makes the API derive client addresses from
// X-Forwarded-For when the immediate peer belongs to proxies.
func WithTrustedProxies(proxies TrustedProxies) Option {
	return func(api *API) { api.trustedProxies = proxies }
}

type clientIPContextKey struct{}

// resolveClientIP computes the client address once per request so that the
// login limiter and every audit record use the same value.
func (a *API) resolveClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := a.trustedProxies.clientAddress(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPContextKey{}, ip)))
	})
}

// clientIP returns the address resolved by resolveClientIP, falling back to the
// TCP peer for requests that did not pass through the middleware.
func clientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPContextKey{}).(string); ok && ip != "" {
		return ip
	}
	return remoteHost(r.RemoteAddr)
}

func remoteHost(remoteAddr string) string {
	if addr, ok := parseHostAddr(remoteAddr); ok {
		return addr.String()
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// clientAddress implements the conventional "rightmost untrusted hop" rule:
// only a trusted peer may supply X-Forwarded-For, and the header is walked
// from the right skipping further trusted proxies. Entries to the left of the
// first untrusted hop were written by the client and may be forged, so they are
// never used. A malformed entry stops the walk and the last verified hop wins.
func (p TrustedProxies) clientAddress(r *http.Request) string {
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return remoteHost(r.RemoteAddr)
	}
	if !p.contains(peer) {
		return peer.String()
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	client := peer
	for index := len(hops) - 1; index >= 0; index-- {
		hop, ok := parseHostAddr(strings.TrimSpace(hops[index]))
		if !ok {
			break
		}
		client = hop
		if !p.contains(hop) {
			break
		}
	}
	return client.String()
}

// parseHostAddr accepts "ip", "ip:port", "[ipv6]" and "[ipv6]:port".
func parseHostAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap().WithZone(""), true
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return addrPort.Addr().Unmap().WithZone(""), true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		if addr, err := netip.ParseAddr(value[1 : len(value)-1]); err == nil {
			return addr.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}
