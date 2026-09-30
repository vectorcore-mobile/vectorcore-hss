package s6a

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/diameter/basedict"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

type s6aTestStore struct {
	sub           *models.Subscriber
	apn           *models.APN
	apns          map[int]*models.APN
	routing       map[int]*models.SubscriberRouting
	lastMMEUpdate *repository.ServingMMEUpdate
}

type noopPeerLookup struct{}

func (n *noopPeerLookup) GetConn(_ string) (diam.Conn, bool) { return nil, false }

func newS6aTestHandlers(store repository.Repository) *Handlers {
	_ = basedict.Load()
	cfg := &config.Config{}
	cfg.HSS.OriginHost = "hss.test.net"
	cfg.HSS.OriginRealm = "test.net"
	cfg.HSS.MCC = "001"
	cfg.HSS.MNC = "01"
	cfg.Roaming.AllowUndefinedNetworks = true
	return NewHandlers(cfg, store, zap.NewNop(), &noopPeerLookup{})
}

func (s *s6aTestStore) GetSubscriberByIMSI(_ context.Context, imsi string) (*models.Subscriber, error) {
	if s.sub != nil && s.sub.IMSI == imsi {
		return s.sub, nil
	}
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetSubscriberByMSISDN(_ context.Context, _ string) (*models.Subscriber, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) UpdateServingMME(_ context.Context, _ string, update *repository.ServingMMEUpdate) error {
	dup := *update
	s.lastMMEUpdate = &dup
	return nil
}

func (s *s6aTestStore) UpdateServingSGSN(_ context.Context, _ string, _ *repository.ServingSGSNUpdate) error {
	return nil
}

func (s *s6aTestStore) UpdateServingVLR(_ context.Context, _ string, _ *repository.ServingVLRUpdate) error {
	return nil
}

func (s *s6aTestStore) UpdateServingMSC(_ context.Context, _ string, _ *repository.ServingMSCUpdate) error {
	return nil
}

func (s *s6aTestStore) UpdateServingAMF(_ context.Context, _ string, _ *repository.ServingAMFUpdate) error {
	return nil
}

func (s *s6aTestStore) GetAPNByID(_ context.Context, apnID int) (*models.APN, error) {
	if s.apns != nil {
		if a, ok := s.apns[apnID]; ok {
			return a, nil
		}
	}
	if s.apn != nil && s.apn.APNID == apnID {
		return s.apn, nil
	}
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetAPNsByIDs(_ context.Context, ids []int) ([]models.APN, error) {
	var out []models.APN
	for _, id := range ids {
		if s.apns != nil {
			if a, ok := s.apns[id]; ok {
				out = append(out, *a)
				continue
			}
		}
		if s.apn != nil && s.apn.APNID == id {
			out = append(out, *s.apn)
		}
	}
	return out, nil
}

func (s *s6aTestStore) GetIMSSubscriberByMSISDN(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetIMSSubscriberByIMSI(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetAUCByIMSI(_ context.Context, _ string) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetAUCByID(_ context.Context, _ int) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) AtomicGetAndIncrementSQN(_ context.Context, _ int, _ int64) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) ResyncSQN(_ context.Context, _ int, _ int64) error { return nil }

func (s *s6aTestStore) GetAlgorithmProfile(_ context.Context, _ int64) (*models.AlgorithmProfile, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) UpsertServingPDUSession(_ context.Context, _ *models.ServingPDUSession) error {
	return nil
}

func (s *s6aTestStore) DeleteServingPDUSession(_ context.Context, _ string, _ int) error { return nil }

func (s *s6aTestStore) ListServingPDUSessions(_ context.Context, _ string) ([]models.ServingPDUSession, error) {
	return nil, nil
}

func (s *s6aTestStore) UpdateIMSSCSCF(_ context.Context, _ string, _ *repository.IMSSCSCFUpdate) error {
	return nil
}

func (s *s6aTestStore) UpdateIMSPCSCF(_ context.Context, _ string, _ *repository.IMSPCSCFUpdate) error {
	return nil
}

func (s *s6aTestStore) GetIFCProfileByID(_ context.Context, _ int) (*models.IFCProfile, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetAPNByName(_ context.Context, _ string) (*models.APN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetAllChargingRules(_ context.Context) ([]models.ChargingRule, error) {
	return nil, nil
}

func (s *s6aTestStore) GetChargingRulesByNames(_ context.Context, _ []string) ([]models.ChargingRule, error) {
	return nil, nil
}

func (s *s6aTestStore) GetChargingRulesByIDs(_ context.Context, _ []int) ([]models.ChargingRule, error) {
	return nil, nil
}

func (s *s6aTestStore) GetTFTsByGroupID(_ context.Context, _ int) ([]models.TFT, error) {
	return nil, nil
}

func (s *s6aTestStore) UpsertServingAPN(_ context.Context, _ *models.ServingAPN) error { return nil }

func (s *s6aTestStore) DeleteServingAPNBySession(_ context.Context, _ string) error { return nil }

func (s *s6aTestStore) GetServingAPNBySession(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetServingAPNByIMSI(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetServingAPNByMSISDN(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetServingAPNByIdentity(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetServingAPNByUEIP(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetServingAPNByUEIPv6Prefix(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetSubscriberRoutingBySubscriberAndAPN(_ context.Context, _, apnID int) (*models.SubscriberRouting, error) {
	if s.routing != nil {
		if r, ok := s.routing[apnID]; ok {
			return r, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) GetSubscriberRoutingsBySubscriberAndAPNs(_ context.Context, _ int, apnIDs []int) ([]models.SubscriberRouting, error) {
	var out []models.SubscriberRouting
	if s.routing == nil {
		return out, nil
	}
	for _, id := range apnIDs {
		if r, ok := s.routing[id]; ok {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *s6aTestStore) GetRoamingRuleByMCCMNC(_ context.Context, _, _ string) (*models.RoamingRules, error) {
	return nil, repository.ErrNotFound
}

func (s *s6aTestStore) UpsertEmergencySubscriber(_ context.Context, _ *models.EmergencySubscriber) error {
	return nil
}

func (s *s6aTestStore) DeleteEmergencySubscriberByIMSI(_ context.Context, _ string) error { return nil }

func (s *s6aTestStore) ListEIR(_ context.Context, _ *[]models.EIR) error { return nil }

func (s *s6aTestStore) EIRNoMatchResponse() int { return 2 }

func (s *s6aTestStore) UpsertIMSIIMEIHistory(_ context.Context, _, _, _, _ string, _ int) error {
	return nil
}

func (s *s6aTestStore) StoreMWD(_ context.Context, _ *models.MessageWaitingData) error {
	return nil
}

func (s *s6aTestStore) GetMWDForIMSI(_ context.Context, _ string) ([]models.MessageWaitingData, error) {
	return nil, nil
}

func (s *s6aTestStore) DeleteMWD(_ context.Context, _, _ string) error { return nil }

func (s *s6aTestStore) InvalidateCache(_ string) {}
func (s *s6aTestStore) InvalidateAPNCache(_ int) {}

func (s *s6aTestStore) ListAllAUC(_ context.Context) ([]models.AUC, error) { return nil, nil }

func (s *s6aTestStore) ListAllSubscribers(_ context.Context) ([]models.Subscriber, error) {
	return nil, nil
}

func (s *s6aTestStore) ListAllIMSSubscribers(_ context.Context) ([]models.IMSSubscriber, error) {
	return nil, nil
}

func (s *s6aTestStore) ListAllServingAPN(_ context.Context) ([]repository.GeoredServingAPN, error) {
	return nil, nil
}

func (s *s6aTestStore) UpsertSubscriber(_ context.Context, _ *models.Subscriber) error { return nil }

func (s *s6aTestStore) DeleteSubscriberByIMSI(_ context.Context, _ string) error { return nil }

func (s *s6aTestStore) UpsertAUC(_ context.Context, _ *models.AUC) error { return nil }

func (s *s6aTestStore) DeleteAUCByID(_ context.Context, _ int) error { return nil }

func (s *s6aTestStore) UpsertAPN(_ context.Context, _ *models.APN) error { return nil }

func (s *s6aTestStore) DeleteAPNByID(_ context.Context, _ int) error { return nil }

func (s *s6aTestStore) UpsertIMSSubscriber(_ context.Context, _ *models.IMSSubscriber) error {
	return nil
}

func (s *s6aTestStore) DeleteIMSSubscriberByMSISDN(_ context.Context, _ string) error { return nil }

func (s *s6aTestStore) UpsertEIR(_ context.Context, _ *models.EIR) error { return nil }

func (s *s6aTestStore) DeleteEIRByID(_ context.Context, _ int) error { return nil }

func buildSMSAwareULR(t *testing.T, imsi, mmeNumber string, withFeature bool) *diam.Message {
	t.Helper()

	plmn := []byte{0x00, 0xF1, 0x10}
	req := diam.NewRequest(diam.UpdateLocation, AppIDS6a, nil)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("test-session"))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("mme.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.UserName, avp.Mbit, 0, datatype.UTF8String(imsi))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.RATType, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(1004))
	req.NewAVP(avp.ULRFlags, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(ULRFlagS6aIndicator|ULRFlagSMSOnlyIndication))
	req.NewAVP(avp.VisitedPLMNID, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(plmn))

	if withFeature {
		req.NewAVP(avp.SupportedFeatures, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avp.VendorID, avp.Mbit, 0, datatype.Unsigned32(Vendor3GPP)),
			diam.NewAVP(avp.FeatureListID, avp.Vbit, Vendor3GPP, datatype.Unsigned32(FeatureListIDSMSInMME)),
			diam.NewAVP(avp.FeatureList, avp.Vbit, Vendor3GPP, datatype.Unsigned32(FeatureBitSMSInMME)),
		}})
	}

	if mmeNumber != "" {
		enc, err := encodeMSISDN(mmeNumber)
		if err != nil {
			t.Fatalf("encode MME number: %v", err)
		}
		req.NewAVP(avpMMENumberForMTSMS, avp.Vbit, Vendor3GPP, datatype.OctetString(enc))
	}
	req.NewAVP(avpSMSRegisterRequest, avp.Vbit, Vendor3GPP, datatype.Enumerated(SMSRegistrationRequired))

	return req
}

func decodeMessageForTest(t *testing.T, msg *diam.Message) *diam.Message {
	t.Helper()
	raw, err := msg.Serialize()
	if err != nil {
		t.Fatalf("serialize message: %v", err)
	}
	decoded, err := diam.ReadMessage(bytes.NewReader(raw), msg.Dictionary())
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return decoded
}

func requireULAFlags(t *testing.T, msg *diam.Message, want uint32) {
	t.Helper()
	a, err := msg.FindAVP(avp.ULAFlags, Vendor3GPP)
	if err != nil || a == nil {
		t.Fatal("missing ULA-Flags AVP")
	}
	got, ok := a.Data.(datatype.Unsigned32)
	if !ok {
		t.Fatalf("ULA-Flags has unexpected type %T", a.Data)
	}
	if uint32(got) != want {
		t.Fatalf("ULA-Flags got 0x%x want 0x%x", uint32(got), want)
	}
}

func buildNORForTest(t *testing.T, imsi string, alertReason int32) *diam.Message {
	t.Helper()
	req := diam.NewRequest(diam.Notify, AppIDS6a, nil)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("nor-session"))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("mme.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.UserName, avp.Mbit, 0, datatype.UTF8String(imsi))

	alertDef, err := dict.Default.FindAVPWithVendor(AppIDS6a, "Alert-Reason", Vendor3GPP)
	if err != nil {
		t.Fatalf("find Alert-Reason AVP: %v", err)
	}
	req.NewAVP(alertDef.Code, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(alertReason))
	return req
}

func buildNORWithMaximumAvailabilityForTest(t *testing.T, imsi string, alertReason int32, maximumUEAvailabilityTime time.Time) *diam.Message {
	t.Helper()
	req := buildNORForTest(t, imsi, alertReason)
	req.NewAVP(avpMaximumUEAvailabilityTime, avp.Vbit, Vendor3GPP, datatype.Time(maximumUEAvailabilityTime))
	return req
}

func TestULR_SMSRegistrationAccepted(t *testing.T) {
	store := &s6aTestStore{
		sub: &models.Subscriber{
			IMSI:                  "001010000000001",
			AUCID:                 1,
			DefaultAPN:            1,
			APNList:               "1",
			SubscribedRAUTAUTimer: 300,
		},
		apn: &models.APN{
			APNID:                   1,
			APN:                     "internet",
			IPVersion:               0,
			QCI:                     9,
			ARPPriority:             4,
			APNAMBRDown:             1000,
			APNAMBRUp:               1000,
			ChargingCharacteristics: "0800",
		},
	}

	h := newS6aTestHandlers(store)
	req := decodeMessageForTest(t, buildSMSAwareULR(t, store.sub.IMSI, "15551230001", true))
	ans, err := h.ULR(nil, req)
	if err != nil {
		t.Fatalf("ULR returned error: %v", err)
	}
	if store.lastMMEUpdate == nil {
		t.Fatal("expected UpdateServingMME to be called")
	}
	if store.lastMMEUpdate.MMERegisteredForSMS == nil || !*store.lastMMEUpdate.MMERegisteredForSMS {
		t.Fatal("expected MMERegisteredForSMS=true")
	}
	if store.lastMMEUpdate.MMENumberForMTSMS == nil || *store.lastMMEUpdate.MMENumberForMTSMS != "15551230001" {
		t.Fatalf("unexpected MME number: %+v", store.lastMMEUpdate.MMENumberForMTSMS)
	}
	if store.lastMMEUpdate.SMSRegisterRequest == nil || *store.lastMMEUpdate.SMSRegisterRequest != int(SMSRegistrationRequired) {
		t.Fatalf("unexpected SMS register request: %+v", store.lastMMEUpdate.SMSRegisterRequest)
	}

	requireULAFlags(t, ans, ULAFlagSeparationIndication|ULAFlagMMERegisteredForSMS)
}

func TestULR_SMSRegistrationRejectedWithoutMMENumber(t *testing.T) {
	store := &s6aTestStore{
		sub: &models.Subscriber{
			IMSI:                  "001010000000002",
			AUCID:                 1,
			DefaultAPN:            1,
			APNList:               "1",
			SubscribedRAUTAUTimer: 300,
		},
		apn: &models.APN{
			APNID:                   1,
			APN:                     "internet",
			IPVersion:               0,
			QCI:                     9,
			ARPPriority:             4,
			APNAMBRDown:             1000,
			APNAMBRUp:               1000,
			ChargingCharacteristics: "0800",
		},
	}

	h := newS6aTestHandlers(store)
	req := decodeMessageForTest(t, buildSMSAwareULR(t, store.sub.IMSI, "", true))
	ans, err := h.ULR(nil, req)
	if err != nil {
		t.Fatalf("ULR returned error: %v", err)
	}
	if store.lastMMEUpdate == nil {
		t.Fatal("expected UpdateServingMME to be called")
	}
	if store.lastMMEUpdate.MMERegisteredForSMS != nil && *store.lastMMEUpdate.MMERegisteredForSMS {
		t.Fatal("did not expect SMS registration acceptance")
	}

	requireULAFlags(t, ans, ULAFlagSeparationIndication)
}

func TestNOR_UEPresentTriggersSubscriberReadyCallback(t *testing.T) {
	store := &s6aTestStore{}
	h := newS6aTestHandlers(store)

	type callback struct {
		imsi                      string
		trigger                   AlertTrigger
		maximumUEAvailabilityTime *time.Time
	}
	gotCh := make(chan callback, 1)
	h.WithOnSubscriberReady(func(imsi string, trigger AlertTrigger, maximumUEAvailabilityTime *time.Time) {
		gotCh <- callback{imsi: imsi, trigger: trigger, maximumUEAvailabilityTime: maximumUEAvailabilityTime}
	})

	req := decodeMessageForTest(t, buildNORForTest(t, "001010000000010", AlertReasonUEPresent))
	if _, err := h.NOR(nil, req); err != nil {
		t.Fatalf("NOR returned error: %v", err)
	}

	var got callback
	select {
	case got = <-gotCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber-ready callback")
	}
	if got.imsi != "001010000000010" {
		t.Fatalf("callback IMSI = %q, want %q", got.imsi, "001010000000010")
	}
	if got.trigger != AlertTriggerUserAvailable {
		t.Fatalf("callback trigger = %q, want %q", got.trigger, AlertTriggerUserAvailable)
	}
}

func TestNOR_UEMemoryAvailableTriggersSubscriberReadyCallback(t *testing.T) {
	store := &s6aTestStore{}
	h := newS6aTestHandlers(store)

	type callback struct {
		imsi                      string
		trigger                   AlertTrigger
		maximumUEAvailabilityTime *time.Time
	}
	gotCh := make(chan callback, 1)
	h.WithOnSubscriberReady(func(imsi string, trigger AlertTrigger, maximumUEAvailabilityTime *time.Time) {
		gotCh <- callback{imsi: imsi, trigger: trigger, maximumUEAvailabilityTime: maximumUEAvailabilityTime}
	})

	req := decodeMessageForTest(t, buildNORForTest(t, "001010000000011", AlertReasonUEMemoryAvailable))
	if _, err := h.NOR(nil, req); err != nil {
		t.Fatalf("NOR returned error: %v", err)
	}

	var got callback
	select {
	case got = <-gotCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber-ready callback")
	}
	if got.imsi != "001010000000011" {
		t.Fatalf("callback IMSI = %q, want %q", got.imsi, "001010000000011")
	}
	if got.trigger != AlertTriggerMemoryAvailable {
		t.Fatalf("callback trigger = %q, want %q", got.trigger, AlertTriggerMemoryAvailable)
	}
}

func TestNOR_PassesMaximumUEAvailabilityTimeToCallback(t *testing.T) {
	store := &s6aTestStore{}
	h := newS6aTestHandlers(store)

	type callback struct {
		imsi                      string
		trigger                   AlertTrigger
		maximumUEAvailabilityTime *time.Time
	}
	gotCh := make(chan callback, 1)
	h.WithOnSubscriberReady(func(imsi string, trigger AlertTrigger, maximumUEAvailabilityTime *time.Time) {
		gotCh <- callback{imsi: imsi, trigger: trigger, maximumUEAvailabilityTime: maximumUEAvailabilityTime}
	})

	maxAvail := time.Unix(1735689600, 0).UTC()
	req := decodeMessageForTest(t, buildNORWithMaximumAvailabilityForTest(t, "001010000000013", AlertReasonUEMemoryAvailable, maxAvail))
	if _, err := h.NOR(nil, req); err != nil {
		t.Fatalf("NOR returned error: %v", err)
	}

	var got callback
	select {
	case got = <-gotCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber-ready callback")
	}
	if got.maximumUEAvailabilityTime == nil {
		t.Fatal("callback maximumUEAvailabilityTime = nil, want value")
	}
	if !got.maximumUEAvailabilityTime.UTC().Equal(maxAvail) {
		t.Fatalf("callback maximumUEAvailabilityTime = %s, want %s", got.maximumUEAvailabilityTime.UTC(), maxAvail)
	}
}

func TestNOR_UnknownAlertReasonDoesNotTriggerCallback(t *testing.T) {
	store := &s6aTestStore{}
	h := newS6aTestHandlers(store)

	triggered := make(chan struct{}, 1)
	h.WithOnSubscriberReady(func(string, AlertTrigger, *time.Time) {
		triggered <- struct{}{}
	})

	req := decodeMessageForTest(t, buildNORForTest(t, "001010000000012", 99))
	if _, err := h.NOR(nil, req); err != nil {
		t.Fatalf("NOR returned error: %v", err)
	}

	select {
	case <-triggered:
		t.Fatal("unexpected subscriber-ready callback for unknown Alert-Reason")
	default:
	}
}
