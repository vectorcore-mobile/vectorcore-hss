package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

// idrRecorder records IDRs. updateSubscriber sends them from a goroutine,
// so tests must read the calls through waitCalls, not directly.
type idrRecorder struct {
	mu    sync.Mutex
	imsis []string
}

func (r *idrRecorder) SendIDRByIMSI(_ context.Context, imsi string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.imsis = append(r.imsis, imsi)
	return nil
}

// waitCalls returns the recorded IMSIs once want calls have arrived, or
// whatever arrived by the timeout. With want == 0 it waits the full settle
// period so a late IDR is still caught.
func (r *idrRecorder) waitCalls(want int) []string {
	timeout := 2 * time.Second
	if want == 0 {
		timeout = 200 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		got := append([]string(nil), r.imsis...)
		r.mu.Unlock()
		if (want > 0 && len(got) >= want) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpdateSubscriberSendsIDROnAccessRestrictionChange(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	idr := &idrRecorder{}
	s.WithIDR(idr)

	enabled := true
	oldARD := uint32(64)
	newARD := uint32(320)
	sub := models.Subscriber{
		IMSI:                  "001010000000101",
		Enabled:               &enabled,
		AUCID:                 1,
		DefaultAPN:            1,
		APNList:               "1",
		AccessRestrictionData: &oldARD,
	}
	mustCreate(t, s.db, &sub)

	updated := sub
	updated.AccessRestrictionData = &newARD
	if _, err := s.updateSubscriber(ctx, &SubscriberUpdateInput{ID: sub.SubscriberID, Body: &updated}); err != nil {
		t.Fatalf("update subscriber: %v", err)
	}

	if calls := idr.waitCalls(1); len(calls) != 1 || calls[0] != sub.IMSI {
		t.Fatalf("IDR calls = %#v, want [%q]", calls, sub.IMSI)
	}
}

func TestUpdateSubscriberSkipsIDRWhenAccessRestrictionUnchanged(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	idr := &idrRecorder{}
	s.WithIDR(idr)

	enabled := true
	ard := uint32(64)
	sub := models.Subscriber{
		IMSI:                  "001010000000102",
		Enabled:               &enabled,
		AUCID:                 1,
		DefaultAPN:            1,
		APNList:               "1",
		AccessRestrictionData: &ard,
	}
	mustCreate(t, s.db, &sub)

	updated := sub
	if _, err := s.updateSubscriber(ctx, &SubscriberUpdateInput{ID: sub.SubscriberID, Body: &updated}); err != nil {
		t.Fatalf("update subscriber: %v", err)
	}

	if calls := idr.waitCalls(0); len(calls) != 0 {
		t.Fatalf("unexpected IDR calls: %#v", calls)
	}
}

func TestUpdateSubscriberSkipsIDRWhenDisabled(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	idr := &idrRecorder{}
	s.WithIDR(idr)

	enabled := true
	oldARD := uint32(64)
	newARD := uint32(320)
	sub := models.Subscriber{
		IMSI:                  "001010000000103",
		Enabled:               &enabled,
		AUCID:                 1,
		DefaultAPN:            1,
		APNList:               "1",
		AccessRestrictionData: &oldARD,
	}
	mustCreate(t, s.db, &sub)

	disabled := false
	updated := sub
	updated.Enabled = &disabled
	updated.AccessRestrictionData = &newARD
	if _, err := s.updateSubscriber(ctx, &SubscriberUpdateInput{ID: sub.SubscriberID, Body: &updated}); err != nil {
		t.Fatalf("update subscriber: %v", err)
	}

	if calls := idr.waitCalls(0); len(calls) != 0 {
		t.Fatalf("unexpected IDR calls: %#v", calls)
	}
}

// TestCreateSubscriberRejectsWBEUTRANLTEMConflict verifies the TS 29.272
// §7.3.31 NOTE 2 rule: bit 4 (WB-E-UTRAN Not Allowed) cannot be combined with
// bit 11 (LTE-M Not Allowed) or bit 12 (WB-E-UTRAN Except LTE-M Not Allowed).
func TestCreateSubscriberRejectsWBEUTRANLTEMConflict(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	cases := []struct {
		name string
		imsi string
		ard  uint32
	}{
		{"bit4_plus_bit11", "001010000000230", models.ARDWBEUTRANNotAllowed | models.ARDLTEMNotAllowed},
		{"bit4_plus_bit12", "001010000000231", models.ARDWBEUTRANNotAllowed | models.ARDWBEUTRANExceptLTEMNotAllowed},
		{"bit4_plus_both", "001010000000232", models.ARDWBEUTRANNotAllowed | models.ARDLTEMNotAllowed | models.ARDWBEUTRANExceptLTEMNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled := true
			ard := tc.ard
			sub := models.Subscriber{
				IMSI:                  tc.imsi,
				Enabled:               &enabled,
				AUCID:                 1,
				DefaultAPN:            1,
				APNList:               "1",
				AccessRestrictionData: &ard,
			}
			if _, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub}); err == nil {
				t.Fatalf("expected validation error for ARD 0x%08x, got nil", tc.ard)
			}
		})
	}
}

// TestCreateSubscriberAllowsLTEMWithoutWBEUTRANConflict confirms the LTE-M
// bits are accepted on their own (bit 4 clear) — only the combination with
// bit 4 is invalid.
func TestCreateSubscriberAllowsLTEMWithoutWBEUTRANConflict(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	ard := models.ARDLTEMNotAllowed | models.ARDWBEUTRANExceptLTEMNotAllowed
	sub := models.Subscriber{
		IMSI:                  "001010000000233",
		Enabled:               &enabled,
		AUCID:                 1,
		DefaultAPN:            1,
		APNList:               "1",
		AccessRestrictionData: &ard,
	}
	if _, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestCreateSubscriberDefaultsToServiceGranted(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	sub := models.Subscriber{
		IMSI:       "001010000000200",
		Enabled:    &enabled,
		AUCID:      1,
		DefaultAPN: 1,
		APNList:    "1",
	}
	out, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub})
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	if out.Body.SubscriberStatus != models.SubscriberStatusServiceGranted {
		t.Fatalf("subscriber_status = %d, want SERVICE_GRANTED (0)", out.Body.SubscriberStatus)
	}
	if out.Body.OperatorDeterminedBarring != 0 {
		t.Fatalf("operator_determined_barring = %d, want 0", out.Body.OperatorDeterminedBarring)
	}
}

func TestCreateSubscriberRejectsInvalidSubscriberStatus(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	sub := models.Subscriber{
		IMSI:             "001010000000201",
		Enabled:          &enabled,
		AUCID:            1,
		DefaultAPN:       1,
		APNList:          "1",
		SubscriberStatus: 2, // undefined per TS 29.272 §7.3.29
	}
	if _, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub}); err == nil {
		t.Fatal("expected validation error for undefined subscriber_status, got nil")
	}
}

func TestCreateSubscriberRejectsReservedODBBits(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	sub := models.Subscriber{
		IMSI:                      "001010000000202",
		Enabled:                   &enabled,
		AUCID:                     1,
		DefaultAPN:                1,
		APNList:                   "1",
		SubscriberStatus:          models.SubscriberStatusOperatorDeterminedBarring,
		OperatorDeterminedBarring: 1 << 9, // reserved bit, not defined in §7.3.30
	}
	if _, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub}); err == nil {
		t.Fatal("expected validation error for reserved ODB bit, got nil")
	}
}

// TestCreateSubscriberODBBitmaskCombinesCorrectly verifies bits 0+3 encode to
// 0x9 per the handoff's worked example, and round-trips through create/read.
func TestCreateSubscriberODBBitmaskCombinesCorrectly(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	sub := models.Subscriber{
		IMSI:             "001010000000203",
		Enabled:          &enabled,
		AUCID:            1,
		DefaultAPN:       1,
		APNList:          "1",
		SubscriberStatus: models.SubscriberStatusOperatorDeterminedBarring,
		OperatorDeterminedBarring: models.ODBAllPacketOrientedServicesBarred |
			models.ODBBarringOfAllOutgoingCalls,
	}
	out, err := s.createSubscriber(ctx, &SubscriberCreateInput{Body: &sub})
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	if out.Body.OperatorDeterminedBarring != 0x00000009 {
		t.Fatalf("operator_determined_barring = 0x%08x, want 0x00000009", out.Body.OperatorDeterminedBarring)
	}
}

func TestUpdateSubscriberClearsODBWhenServiceGranted(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	enabled := true
	sub := models.Subscriber{
		IMSI:                      "001010000000204",
		Enabled:                   &enabled,
		AUCID:                     1,
		DefaultAPN:                1,
		APNList:                   "1",
		SubscriberStatus:          models.SubscriberStatusOperatorDeterminedBarring,
		OperatorDeterminedBarring: models.ODBAllPacketOrientedServicesBarred,
	}
	mustCreate(t, s.db, &sub)

	updated := sub
	updated.SubscriberStatus = models.SubscriberStatusServiceGranted
	// Simulate a stale/leftover mask still present in the request body — the
	// server must clear it rather than trust the client.
	updated.OperatorDeterminedBarring = models.ODBAllPacketOrientedServicesBarred
	out, err := s.updateSubscriber(ctx, &SubscriberUpdateInput{ID: sub.SubscriberID, Body: &updated})
	if err != nil {
		t.Fatalf("update subscriber: %v", err)
	}
	if out.Body.OperatorDeterminedBarring != 0 {
		t.Fatalf("operator_determined_barring = %d, want 0 after reverting to SERVICE_GRANTED", out.Body.OperatorDeterminedBarring)
	}
}

func TestUpdateSubscriberSendsIDROnSubscriberStatusChange(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	idr := &idrRecorder{}
	s.WithIDR(idr)

	enabled := true
	sub := models.Subscriber{
		IMSI:       "001010000000205",
		Enabled:    &enabled,
		AUCID:      1,
		DefaultAPN: 1,
		APNList:    "1",
	}
	mustCreate(t, s.db, &sub)

	updated := sub
	updated.SubscriberStatus = models.SubscriberStatusOperatorDeterminedBarring
	updated.OperatorDeterminedBarring = models.ODBBarringOfAllOutgoingCalls
	if _, err := s.updateSubscriber(ctx, &SubscriberUpdateInput{ID: sub.SubscriberID, Body: &updated}); err != nil {
		t.Fatalf("update subscriber: %v", err)
	}

	if calls := idr.waitCalls(1); len(calls) != 1 || calls[0] != sub.IMSI {
		t.Fatalf("IDR calls = %#v, want [%q]", calls, sub.IMSI)
	}
}
