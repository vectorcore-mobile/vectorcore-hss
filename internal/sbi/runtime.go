package sbi

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/url"
)

// NewNFInstanceID returns a random RFC 4122 version 4 UUID string.
func NewNFInstanceID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // crypto/rand.Read is expected to succeed on supported platforms
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ResolveAdvertiseIP picks the IPv4 address advertised to the NRF in the NF
// profile. Preference order:
//  1. advertise, when it is a concrete IPv4 address
//  2. bind, when it is a concrete IPv4 address
//  3. the local source address the kernel selects to reach the NRF host
//  4. 127.0.0.1
func ResolveAdvertiseIP(advertise, bind, nrfAddress string) string {
	if ip := concreteIPv4(advertise); ip != "" {
		return ip
	}
	if ip := concreteIPv4(bind); ip != "" {
		return ip
	}
	if ip := sourceIPv4Toward(nrfHostPort(nrfAddress)); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func concreteIPv4(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsUnspecified() {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ""
}

// nrfHostPort extracts host:port from an NRF base URL such as
// "http://10.0.0.5:7777". A missing port defaults from the scheme.
func nrfHostPort(nrfAddress string) string {
	u, err := url.Parse(nrfAddress)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// sourceIPv4Toward returns the local IPv4 address used to route to target.
// The UDP dial sends no packets; it only performs a route lookup.
func sourceIPv4Toward(target string) string {
	if target == "" {
		return ""
	}
	conn, err := net.Dial("udp4", target)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return concreteIPv4(addr.IP.String())
}

// SchemeFromTLS returns the SBI scheme implied by the TLS settings.
func SchemeFromTLS(certFile string) string {
	if certFile != "" {
		return "https"
	}
	return "http"
}
