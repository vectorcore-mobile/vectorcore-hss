package avputil

import (
	"bytes"
	"net"
	"testing"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
)

func rawPrefix(addr string, prefixLen int) []byte {
	ip := net.ParseIP(addr).To16()
	return append([]byte{0, byte(prefixLen)}, ip[:(prefixLen+7)/8]...)
}

func TestIPv6BindingKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    []byte
		want   string
		wantOK bool
	}{
		{"Gx /64 from the P-GW", rawPrefix("2001:db8:46:1a::", 64), "2001:db8:46:1a::/64", true},
		{"Rx /128 UE address", rawPrefix("2001:db8:46:1a:1234:5678:9abc:def0", 128), "2001:db8:46:1a::/64", true},
		{"/96 inside the /64", rawPrefix("2001:db8:46:1a:1::", 96), "2001:db8:46:1a::/64", true},
		{"/56 does not identify one PDN", rawPrefix("2001:db8:46::", 56), "", false},
		{"truncated value", []byte{0, 64, 0x20, 0x01}, "", false},
		{"too short", []byte{0}, "", false},
	} {
		got, ok := IPv6BindingKey(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("%s: got %q/%v, want %q/%v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestFramedIPv6PrefixRawFindsAVP97(t *testing.T) {
	raw := rawPrefix("2001:db8:46:1a::", 64)
	msg := diam.NewRequest(diam.CreditControl, 16777238, dict.Default)
	msg.NewAVP(avp.FramedIPv6Prefix, avp.Mbit, 0, datatype.OctetString(raw))
	encoded, err := msg.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	decoded, err := diam.ReadMessage(bytesReader(encoded), dict.Default)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	got := FramedIPv6PrefixRaw(decoded)
	if key, ok := IPv6BindingKey(got); !ok || key != "2001:db8:46:1a::/64" {
		t.Fatalf("decoded Framed-IPv6-Prefix %x gave key %q/%v", got, key, ok)
	}
	if FramedIPv6PrefixRaw(diam.NewRequest(diam.CreditControl, 16777238, dict.Default)) != nil {
		t.Fatal("absent Framed-IPv6-Prefix returned a value")
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
