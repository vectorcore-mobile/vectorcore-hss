package sbi

import "testing"

func TestResolveAdvertiseIPPreference(t *testing.T) {
	tests := []struct {
		name      string
		advertise string
		bind      string
		nrf       string
		want      string
	}{
		{"advertise wins over bind", "192.0.2.10", "10.0.2.117", "http://127.0.0.10:7777", "192.0.2.10"},
		{"concrete IPv4 bind", "", "10.0.2.117", "http://127.0.0.10:7777", "10.0.2.117"},
		{"IPv4-mapped bind", "", "::ffff:10.0.2.117", "", "10.0.2.117"},
		{"invalid advertise falls back to bind", "not-an-ip", "10.0.2.117", "", "10.0.2.117"},
		{"IPv6 advertise ignored", "2001:db8::1", "10.0.2.117", "", "10.0.2.117"},
		{"wildcard v6 bind routes toward NRF", "", "::", "http://127.0.0.10:7777", "127.0.0.1"},
		{"wildcard v4 bind routes toward NRF", "", "0.0.0.0", "http://127.0.0.10:7777", "127.0.0.1"},
		{"IPv6 bind routes toward NRF", "", "2001:db8::5", "http://127.0.0.10:7777", "127.0.0.1"},
		{"NRF without port", "", "::", "http://127.0.0.10", "127.0.0.1"},
		{"no NRF falls back to loopback", "", "::", "", "127.0.0.1"},
		{"unparseable NRF falls back to loopback", "", "::", "://bad", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAdvertiseIP(tt.advertise, tt.bind, tt.nrf); got != tt.want {
				t.Fatalf("ResolveAdvertiseIP(%q, %q, %q) = %q, want %q", tt.advertise, tt.bind, tt.nrf, got, tt.want)
			}
		})
	}
}

func TestNRFHostPort(t *testing.T) {
	tests := map[string]string{
		"http://10.0.0.5:7777":       "10.0.0.5:7777",
		"http://nrf.local":           "nrf.local:80",
		"https://nrf.local":          "nrf.local:443",
		"http://[2001:db8::1]:7777/": "[2001:db8::1]:7777",
		"":                           "",
	}
	for in, want := range tests {
		if got := nrfHostPort(in); got != want {
			t.Fatalf("nrfHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}
