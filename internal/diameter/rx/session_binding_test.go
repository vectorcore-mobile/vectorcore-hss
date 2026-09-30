package rx

import (
	"context"
	"net"
	"testing"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

type bindingStore struct {
	subscribers []models.Subscriber
	serving     []models.ServingAPN // ordered oldest first, like serving_apn_id
}

func (s *bindingStore) GetServingAPNByUEIP(_ context.Context, ueIP string) (*models.ServingAPN, error) {
	for i := range s.serving {
		if s.serving[i].UEIP != nil && *s.serving[i].UEIP == ueIP {
			return &s.serving[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (s *bindingStore) GetServingAPNByUEIPv6Prefix(_ context.Context, prefix string) (*models.ServingAPN, error) {
	for i := range s.serving {
		if s.serving[i].UEIPv6Prefix != nil && *s.serving[i].UEIPv6Prefix == prefix {
			return &s.serving[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

// GetServingAPNByIdentity mirrors the store: the subscriber's oldest active
// session, whatever its APN.
func (s *bindingStore) GetServingAPNByIdentity(ctx context.Context, identity string) (*models.ServingAPN, error) {
	sub, err := s.GetSubscriberByIMSI(ctx, identity)
	if err != nil {
		if sub, err = s.GetSubscriberByMSISDN(ctx, identity); err != nil {
			return nil, repository.ErrNotFound
		}
	}
	for i := range s.serving {
		if s.serving[i].SubscriberID == sub.SubscriberID && s.serving[i].PCRFSessionID != nil {
			return &s.serving[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (s *bindingStore) GetSubscriberByIMSI(_ context.Context, imsi string) (*models.Subscriber, error) {
	for i := range s.subscribers {
		if s.subscribers[i].IMSI == imsi {
			return &s.subscribers[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (s *bindingStore) GetSubscriberByMSISDN(_ context.Context, msisdn string) (*models.Subscriber, error) {
	for i := range s.subscribers {
		if s.subscribers[i].MSISDN != nil && *s.subscribers[i].MSISDN == msisdn {
			return &s.subscribers[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func strPtr(s string) *string { return &s }

func servingRow(subscriberID int, apn, ip, session string) models.ServingAPN {
	return models.ServingAPN{
		SubscriberID:   subscriberID,
		APNName:        apn,
		UEIP:           strPtr(ip),
		PCRFSessionID:  strPtr(session),
		ServingPGWPeer: strPtr("smf.epc.mnc099.mcc246.3gppnetwork.org"),
	}
}

// Field capture: UE 246990200000011 had an internet Gx session (;50,
// 10.45.0.27) and an IMS Gx session (;51, 10.46.0.26). The P-CSCF's AAR
// carried Framed-IP 10.46.0.26 but the RAR went to ;50, so the P-GW created
// the IMS bearers on the internet PDN and the UE rejected them (ESM #44).
func newCaptureBindingStore() *bindingStore {
	return &bindingStore{
		subscribers: []models.Subscriber{
			{SubscriberID: 7, IMSI: "246990200000011", MSISDN: strPtr("200000011")},
			{SubscriberID: 8, IMSI: "246990200000010", MSISDN: strPtr("200000010")},
		},
		serving: []models.ServingAPN{
			servingRow(7, "internet", "10.45.0.27", "smf;1790704676;50;app_gx"),
			servingRow(7, "ims", "10.46.0.26", "smf;1790704676;51;app_gx"),
			servingRow(8, "internet", "10.45.0.28", "smf;1790704676;52;app_gx"),
			servingRow(8, "ims", "10.46.0.27", "smf;1790704676;53;app_gx"),
		},
	}
}

func TestBindRxToGxSession(t *testing.T) {
	ip := func(s string) []byte { return net.ParseIP(s).To4() }
	tests := []struct {
		name        string
		identity    string
		framedIP    []byte
		wantSession string
		wantBinding string
	}{
		{name: "IMS UE IP binds to IMS session", identity: "246990200000011", framedIP: ip("10.46.0.26"),
			wantSession: "smf;1790704676;51;app_gx", wantBinding: rxBindingUEIP},
		{name: "second UE binds to its own IMS session", identity: "246990200000010", framedIP: ip("10.46.0.27"),
			wantSession: "smf;1790704676;53;app_gx", wantBinding: rxBindingUEIP},
		{name: "MSISDN identity with UE IP", identity: "200000011", framedIP: ip("10.46.0.26"),
			wantSession: "smf;1790704676;51;app_gx", wantBinding: rxBindingUEIP},
		{name: "unresolvable identity trusts UE IP", identity: "sip-local-part", framedIP: ip("10.46.0.26"),
			wantSession: "smf;1790704676;51;app_gx", wantBinding: rxBindingUEIP},
		{name: "no Framed-IP falls back to identity", identity: "246990200000011",
			wantSession: "smf;1790704676;50;app_gx", wantBinding: rxBindingIdentity},
		{name: "unknown UE IP falls back to identity", identity: "246990200000011", framedIP: ip("10.46.9.9"),
			wantSession: "smf;1790704676;50;app_gx", wantBinding: rxBindingIdentity},
		{name: "UE IP owned by another subscriber is not used", identity: "246990200000011", framedIP: ip("10.46.0.27"),
			wantSession: "smf;1790704676;50;app_gx", wantBinding: rxBindingIdentity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, binding, err := bindRxToGxSession(context.Background(), newCaptureBindingStore(), tt.identity, tt.framedIP, nil)
			if err != nil {
				t.Fatalf("bindRxToGxSession: %v", err)
			}
			if got := *rec.PCRFSessionID; got != tt.wantSession {
				t.Fatalf("Gx session got %q, want %q", got, tt.wantSession)
			}
			if binding != tt.wantBinding {
				t.Fatalf("binding got %q, want %q", binding, tt.wantBinding)
			}
		})
	}
}

func TestBindRxToGxSessionUnknownSubscriber(t *testing.T) {
	_, _, err := bindRxToGxSession(context.Background(), newCaptureBindingStore(), "001010000000001", nil, nil)
	if err != repository.ErrNotFound {
		t.Fatalf("err got %v, want ErrNotFound", err)
	}
}

// framedIPv6Prefix encodes a Framed-IPv6-Prefix value (RFC 3162 §2.3).
func framedIPv6Prefix(addr string, prefixLen int) []byte {
	ip := net.ParseIP(addr).To16()
	n := (prefixLen + 7) / 8
	return append([]byte{0, byte(prefixLen)}, ip[:n]...)
}

// H4: an IPv6-only IMS PDN has no Framed-IP-Address; the AAR carries the
// UE's IPv6 address and must still bind to that PDN's Gx session, not to
// the subscriber's oldest (internet) session.
func TestBindRxToGxSessionIPv6(t *testing.T) {
	store := &bindingStore{
		subscribers: []models.Subscriber{{SubscriberID: 7, IMSI: "246990200000011", MSISDN: strPtr("200000011")}},
		serving: []models.ServingAPN{
			servingRow(7, "internet", "10.45.0.27", "smf;1790704676;50;app_gx"),
			{
				SubscriberID:   7,
				APNName:        "ims",
				UEIPv6Prefix:   strPtr("2001:db8:46:1a::/64"),
				PCRFSessionID:  strPtr("smf;1790704676;51;app_gx"),
				ServingPGWPeer: strPtr("smf.epc.mnc099.mcc246.3gppnetwork.org"),
			},
		},
	}
	ctx := context.Background()

	rec, binding, err := bindRxToGxSession(ctx, store, "246990200000011", nil, framedIPv6Prefix("2001:db8:46:1a:1234:5678:9abc:def0", 128))
	if err != nil {
		t.Fatalf("bindRxToGxSession: %v", err)
	}
	if *rec.PCRFSessionID != "smf;1790704676;51;app_gx" || binding != rxBindingUEIPv6 {
		t.Fatalf("IPv6 AAR bound to %q via %q, want IMS session via %q", *rec.PCRFSessionID, binding, rxBindingUEIPv6)
	}

	// IPv4 is tried first when the AAR carries both.
	rec, binding, err = bindRxToGxSession(ctx, store, "246990200000011", net.ParseIP("10.45.0.27").To4(), framedIPv6Prefix("2001:db8:46:1a::1", 128))
	if err != nil || binding != rxBindingUEIP || *rec.PCRFSessionID != "smf;1790704676;50;app_gx" {
		t.Fatalf("dual AAR got session %v binding %q err %v, want IPv4 match first", rec.PCRFSessionID, binding, err)
	}

	// An address outside every stored /64 falls back to identity.
	_, binding, err = bindRxToGxSession(ctx, store, "246990200000011", nil, framedIPv6Prefix("2001:db8:99::1", 128))
	if err != nil || binding != rxBindingIdentity {
		t.Fatalf("unknown IPv6 got binding %q err %v, want identity fallback", binding, err)
	}
}
