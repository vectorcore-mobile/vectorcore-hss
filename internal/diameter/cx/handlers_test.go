package cx

import "testing"

func TestIMSIMSDomainPadsTwoDigitMNC(t *testing.T) {
	got := imsIMSDomain("999", "99")
	want := "ims.mnc099.mcc999.3gppnetwork.org"
	if got != want {
		t.Fatalf("imsIMSDomain() = %q, want %q", got, want)
	}
}

// Lab HSS log: LIR for "+16752012832" returned "unknown identity" because the
// "+" of the global number was kept while MSISDNs are stored as digits.
func TestNormalizeMSISDNFromPublicIdentity(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"+16752012832", "16752012832"},
		{"tel:+16752012832", "16752012832"},
		{"tel:+16752012832;phone-context=ims.mnc435.mcc311.3gppnetwork.org", "16752012832"},
		{"sip:+16752012832@ims.mnc435.mcc311.3gppnetwork.org;user=phone", "16752012832"},
		{"sip:16752012832@ims.mnc435.mcc311.3gppnetwork.org", "16752012832"},
		{"tel:16752012832", "16752012832"},
		{"16752012832", "16752012832"},
		{"311435000070570", "311435000070570"},
	} {
		if got := normalizeMSISDN(tc.in); got != tc.want {
			t.Fatalf("normalizeMSISDN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
