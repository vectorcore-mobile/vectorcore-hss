package sbi

import "fmt"

// FormatBitRate converts a bits-per-second value to a TS 29.571 BitRate
// string ("<n> bps|Kbps|Mbps|Gbps|Tbps"). The largest unit that divides the
// value exactly is used, so the result is always an integer and never loses
// precision (e.g. 1000000 → "1 Mbps", 1500000 → "1500 Kbps").
func FormatBitRate(bps int) string {
	if bps <= 0 {
		return "0 bps"
	}
	units := []struct {
		name string
		div  int
	}{
		{"Tbps", 1_000_000_000_000},
		{"Gbps", 1_000_000_000},
		{"Mbps", 1_000_000},
		{"Kbps", 1_000},
	}
	for _, u := range units {
		if bps%u.div == 0 {
			return fmt.Sprintf("%d %s", bps/u.div, u.name)
		}
	}
	return fmt.Sprintf("%d bps", bps)
}
