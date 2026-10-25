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
