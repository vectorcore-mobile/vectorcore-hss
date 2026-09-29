package api

import (
	"fmt"
	"strings"
)

// normalizeProvisionedMSISDN returns the MSISDN in its stored form: digits
// only, at most 15 (TS 23.003 §3.3; carried as TBCD on S6a/Sh per TS 29.329
// §6.3.2, which has no "+"). A leading "+" is the E.164 international-format
// marker, not a digit, and is dropped so the value matches the Cx lookups,
// which strip it from tel:/sip: public identities. Empty stays empty.
func normalizeProvisionedMSISDN(msisdn string) (string, error) {
	s := strings.TrimPrefix(strings.TrimSpace(msisdn), "+")
	if s == "" {
		return "", nil
	}
	if len(s) > 15 {
		return "", fmt.Errorf("msisdn %q: more than 15 digits", msisdn)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("msisdn %q: only digits and an optional leading '+' are allowed", msisdn)
		}
	}
	return s, nil
}

// normalizeProvisionedMSISDNPtr applies normalizeProvisionedMSISDN to an
// optional MSISDN field in place.
func normalizeProvisionedMSISDNPtr(msisdn *string) error {
	if msisdn == nil {
		return nil
	}
	s, err := normalizeProvisionedMSISDN(*msisdn)
	if err != nil {
		return err
	}
	*msisdn = s
	return nil
}
