package sbi

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

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

// ParseBitRate parses a TS 29.571 BitRate string ("<n>[.<frac>] bps|Kbps|
// Mbps|Gbps|Tbps") into bits per second. Units are decimal (1 Kbps = 1000
// bps); a fractional result is truncated.
func ParseBitRate(s string) (uint64, error) {
	num, unit, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return 0, fmt.Errorf("bitrate %q: want \"<value> <unit>\"", s)
	}
	var mult float64
	switch unit {
	case "bps":
		mult = 1
	case "Kbps":
		mult = 1e3
	case "Mbps":
		mult = 1e6
	case "Gbps":
		mult = 1e9
	case "Tbps":
		mult = 1e12
	default:
		return 0, fmt.Errorf("bitrate %q: unknown unit %q", s, unit)
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("bitrate %q: bad value", s)
	}
	bps := v * mult
	if bps >= math.MaxUint64 {
		return 0, fmt.Errorf("bitrate %q: out of range", s)
	}
	return uint64(bps), nil
}
