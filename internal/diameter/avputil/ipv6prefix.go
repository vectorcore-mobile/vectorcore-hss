package avputil

import (
	"net"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
)

// ipv6BindingPrefixLen is the per-PDN IPv6 prefix length the P-GW assigns
// (TS 23.401 §5.3.1.2.2), which is also the Rx↔Gx binding granularity.
const ipv6BindingPrefixLen = 64

// FramedIPv6PrefixRaw returns the raw value of the Framed-IPv6-Prefix AVP
// (code 97, RFC 3162 §2.3) in msg, looked up by code so it does not depend
// on the application dictionary; nil when absent.
func FramedIPv6PrefixRaw(msg *diam.Message) []byte {
	a, err := msg.FindAVP(avp.FramedIPv6Prefix, 0)
	if err != nil || a == nil || a.Data == nil {
		return nil
	}
	return a.Data.Serialize()
}

// IPv6BindingKey turns a Framed-IPv6-Prefix value (Reserved, Prefix-Length,
// Prefix) into the UE's /64, e.g. "2001:db8:1:2::/64". The Gx session stores
// the P-GW's /64 and an Rx AAR carries the UE's address (often /128); both
// map to the same key. ok is false for a malformed value or a prefix shorter
// than /64, which does not identify a single PDN connection.
func IPv6BindingKey(raw []byte) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	prefixLen := int(raw[1])
	if prefixLen < ipv6BindingPrefixLen || prefixLen > 128 {
		return "", false
	}
	n := (prefixLen + 7) / 8
	if len(raw) < 2+n {
		return "", false
	}
	ip := make(net.IP, net.IPv6len)
	copy(ip, raw[2:2+n])
	network := ip.Mask(net.CIDRMask(ipv6BindingPrefixLen, 128))
	return (&net.IPNet{IP: network, Mask: net.CIDRMask(ipv6BindingPrefixLen, 128)}).String(), true
}
