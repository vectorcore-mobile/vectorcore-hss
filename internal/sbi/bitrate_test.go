package sbi

import "testing"

func TestFormatBitRate(t *testing.T) {
	tests := map[int]string{
		0:                 "0 bps",
		-5:                "0 bps",
		1:                 "1 bps",
		999:               "999 bps",
		1000:              "1 Kbps",
		128000:            "128 Kbps",
		999999:            "999999 bps",
		1000000:           "1 Mbps",
		1500000:           "1500 Kbps",
		99999000:          "99999 Kbps",
		100000000:         "100 Mbps",
		1000000000:        "1 Gbps",
		4294967000:        "4294967 Kbps",
		2000000000000:     "2 Tbps",
		1_500_000_000_000: "1500 Gbps",
	}
	for in, want := range tests {
		if got := FormatBitRate(in); got != want {
			t.Errorf("FormatBitRate(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBitRate(t *testing.T) {
	good := map[string]uint64{
		"0 Kbps":     0, // Kamailio N5 sends this when the SDP has no b=AS
		"38 Kbps":    38000,
		"128 Kbps":   128000,
		"1.5 Mbps":   1500000,
		"999 bps":    999,
		"2 Gbps":     2000000000,
		" 64 Kbps ":  64000,
		"0.001 Kbps": 1,
	}
	for in, want := range good {
		got, err := ParseBitRate(in)
		if err != nil || got != want {
			t.Errorf("ParseBitRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "128", "128Kbps", "128 kbps", "-1 Kbps", "x Kbps", "NaN Kbps", "1e300 Tbps"} {
		if got, err := ParseBitRate(in); err == nil {
			t.Errorf("ParseBitRate(%q) = %d, want error", in, got)
		}
	}
	// Round-trip with FormatBitRate.
	for _, bps := range []int{1, 999, 1000, 38000, 128000, 1500000} {
		if got, err := ParseBitRate(FormatBitRate(bps)); err != nil || got != uint64(bps) {
			t.Errorf("round trip %d: got %d, %v", bps, got, err)
		}
	}
}
