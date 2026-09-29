package api

import (
	"context"
	"testing"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

func TestNormalizeProvisionedMSISDN(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{in: "16752012832", want: "16752012832"},
		{in: "+16752012832", want: "16752012832"},
		{in: " +16752012832 ", want: "16752012832"},
		{in: "", want: ""},
		{in: "123456789012345", want: "123456789012345"},
		{in: "1234567890123456", wantErr: true},
		{in: "+1 675 201 2832", wantErr: true},
		{in: "1675-201-2832", wantErr: true},
		{in: "++16752012832", wantErr: true},
		{in: "sip:16752012832", wantErr: true},
	} {
		got, err := normalizeProvisionedMSISDN(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: got %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q err %v, want %q", tc.in, got, err, tc.want)
		}
	}
}

// A "+" typed when provisioning must not reach the database: Cx lookups strip
// it from tel:/sip: identities and would never match a stored "+".
func TestProvisioningStoresMSISDNWithoutPlus(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	msisdn := "+16752012832"
	sub := models.Subscriber{IMSI: "001010000000240", Enabled: &enabled, AUCID: 1, DefaultAPN: 1, APNList: "1", MSISDN: &msisdn}
	out, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub})
	if err != nil {
		t.Fatalf("createSubscriber: %v", err)
	}
	if out.Body.MSISDN == nil || *out.Body.MSISDN != "16752012832" {
		t.Fatalf("subscriber MSISDN got %v, want 16752012832", out.Body.MSISDN)
	}

	ims, err := s.createIMSSubscriber(ctx, &IMSSubscriberCreateInput{Body: &models.IMSSubscriber{MSISDN: "+16752012832"}})
	if err != nil {
		t.Fatalf("createIMSSubscriber: %v", err)
	}
	var stored models.IMSSubscriber
	if err := s.db.First(&stored, ims.Body.IMSSubscriberID).Error; err != nil {
		t.Fatalf("load IMS subscriber: %v", err)
	}
	if stored.MSISDN != "16752012832" {
		t.Fatalf("IMS subscriber MSISDN stored %q, want 16752012832", stored.MSISDN)
	}

	upd := stored
	upd.MSISDN = "+16752012899"
	if _, err := s.updateIMSSubscriber(ctx, &IMSSubscriberUpdateInput{ID: stored.IMSSubscriberID, Body: &upd}); err != nil {
		t.Fatalf("updateIMSSubscriber: %v", err)
	}
	if err := s.db.First(&stored, stored.IMSSubscriberID).Error; err != nil {
		t.Fatalf("reload IMS subscriber: %v", err)
	}
	if stored.MSISDN != "16752012899" {
		t.Fatalf("updated IMS subscriber MSISDN %q, want 16752012899", stored.MSISDN)
	}

	bad := models.IMSSubscriber{MSISDN: "1675-201-2832"}
	if _, err := s.createIMSSubscriber(ctx, &IMSSubscriberCreateInput{Body: &bad}); err == nil {
		t.Fatal("createIMSSubscriber accepted a non-digit MSISDN")
	}
}
