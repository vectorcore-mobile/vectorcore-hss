package s6c

// 3GPP TS 29.338 spec conformance tests for the S6c interface.
//
// References:
//   TS 29.338 v15  — Diameter-based SMS in EPC (S6c interface)
//   TS 23.003 §12.1 — TBCD encoding for MSISDN / E.164 numbers
//
// Coverage:
//   SRI-SM (Send-Routing-Info-for-SM, cmd 8388647)
//     §5.3.2.1 — Attached subscriber: return Serving-Node with MME-Name
//     §5.3.2.2 — Absent subscriber: return MWD-Status with MNRF bit set
//     Lookup by IMSI (User-Name field) instead of MSISDN
//     Unknown subscriber → Experimental-Result 5001 (User-Unknown)
//     MSISDN echoed back in response (encoded as TBCD, TS 23.003 §12.1)
//
//   RSDS (Report-SM-Delivery-Status, cmd 8388649)
//     §5.3.2.4 SuccessfulTransfer (cause=2) → delete MWD, Result-Code 2001
//     §5.3.2.4 AbsentUser (cause=1)         → store MWD MNRF, MWD-Status returned
//     §5.3.2.4 MemoryCapacityExceeded (0)   → store MWD MCEF, MWD-Status returned
//     §5.3.2.4 SM-Delivery-Outcome absent   → default to AbsentUser / MNRF
//     Unknown subscriber → Experimental-Result 5001
//     SGSN delivery-outcome node type
//
//   TBCD encoding (TS 23.003 §12.1)
//     Even-length digit string
//     Odd-length digit string (padded with 0xF)
//     Round-trip: encode → decode → original string
//
//   parseDeliveryOutcome (internal)
//     MME, SGSN, MSC, IP-SM-GW node sub-AVPs
//     Absent SM-Delivery-Outcome → Cause -1

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"testing"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/diameter/sh"
	"github.com/svinson1121/vectorcore-hss/internal/diameter/slh"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// ── test bootstrap ─────────────────────────────────────────────────────────────

func TestMain(m *testing.M) {
	// Load dictionaries in the same order as server.go:
	//   Sh  — defines MSISDN (code 701) and other shared 3GPP AVPs
	//   SLh — defines Serving-Node (2401), MME-Name (2402), MME-Realm (2408), …
	//   S6c — adds its own AVPs; omits those already registered above
	if err := sh.LoadDict(); err != nil {
		panic("s6c tests: load Sh dict: " + err.Error())
	}
	if err := slh.LoadDict(); err != nil {
		panic("s6c tests: load SLh dict: " + err.Error())
	}
	if err := LoadDict(); err != nil {
		panic("s6c tests: load S6c dict: " + err.Error())
	}

	// Match the production startup path by loading the S6c supplement that
	// exposes shared 3GPP MSISDN under the base dictionary fallback.
	if err := LoadMSISDNSupplement(); err != nil {
		panic("s6c tests: load MSISDN supplement dict: " + err.Error())
	}
	os.Exit(m.Run())
}

// newTestHandlers returns a Handlers wired to the given mock store.
func newTestHandlers(store repository.Repository) *Handlers {
	cfg := &config.Config{}
	cfg.HSS.OriginHost = "hss.test.net"
	cfg.HSS.OriginRealm = "test.net"
	return NewHandlers(cfg, store, zap.NewNop(), &noopPeerLookup{})
}

// ── mock PeerLookup ───────────────────────────────────────────────────────────

type noopPeerLookup struct{}

func (n *noopPeerLookup) GetConn(_ string) (diam.Conn, bool) { return nil, false }

// ── mock repository ───────────────────────────────────────────────────────────

// s6cStore is a thin in-memory store for S6c handler tests.
// Only the methods exercised by SRI-SM and RSDS handlers are active;
// everything else returns ErrNotFound or a zero value.
type s6cStore struct {
	byIMSI   map[string]*models.Subscriber
	byMSISDN map[string]*models.Subscriber

	// MWD state
	mwds map[string][]models.MessageWaitingData // keyed by IMSI

	// Call recording
	storedMWD  []storedMWDArgs
	deletedMWD []deletedMWDArgs
}

type storedMWDArgs struct {
	imsi, scAddr, scOriginHost, scOriginRealm string
	smsmiCorrelationID                        *string
	absentUserDiagnosticSM                    *uint32
	lastAlertTrigger                          *string
	mti                                       int
	statusFlags                               uint32
	alertAttemptCount                         uint32
}

type deletedMWDArgs struct {
	imsi, scAddr string
}

func newS6cStore() *s6cStore {
	return &s6cStore{
		byIMSI:   make(map[string]*models.Subscriber),
		byMSISDN: make(map[string]*models.Subscriber),
		mwds:     make(map[string][]models.MessageWaitingData),
	}
}

func (s *s6cStore) addSubscriber(sub *models.Subscriber) {
	s.byIMSI[sub.IMSI] = sub
	if sub.MSISDN != nil {
		s.byMSISDN[*sub.MSISDN] = sub
	}
}

func (s *s6cStore) GetSubscriberByIMSI(_ context.Context, imsi string) (*models.Subscriber, error) {
	if sub, ok := s.byIMSI[imsi]; ok {
		return sub, nil
	}
	return nil, repository.ErrNotFound
}

func (s *s6cStore) GetSubscriberByMSISDN(_ context.Context, msisdn string) (*models.Subscriber, error) {
	if sub, ok := s.byMSISDN[msisdn]; ok {
		return sub, nil
	}
	return nil, repository.ErrNotFound
}

func (s *s6cStore) StoreMWD(_ context.Context, rec *models.MessageWaitingData) error {
	if rec == nil {
		return nil
	}
	s.storedMWD = append(s.storedMWD, storedMWDArgs{
		imsi:                   rec.IMSI,
		scAddr:                 rec.SCAddress,
		scOriginHost:           rec.SCOriginHost,
		scOriginRealm:          rec.SCOriginRealm,
		smsmiCorrelationID:     rec.SMSMICorrelationID,
		absentUserDiagnosticSM: rec.AbsentUserDiagnosticSM,
		lastAlertTrigger:       rec.LastAlertTrigger,
		mti:                    rec.SMRPMTI,
		statusFlags:            rec.MWDStatusFlags,
		alertAttemptCount:      rec.AlertAttemptCount,
	})
	records := s.mwds[rec.IMSI]
	for i := range records {
		if records[i].SCAddress == rec.SCAddress {
			records[i] = *rec
			s.mwds[rec.IMSI] = records
			return nil
		}
	}
	s.mwds[rec.IMSI] = append(records, *rec)
	return nil
}

func (s *s6cStore) GetMWDForIMSI(_ context.Context, imsi string) ([]models.MessageWaitingData, error) {
	return s.mwds[imsi], nil
}

func (s *s6cStore) DeleteMWD(_ context.Context, imsi, scAddr string) error {
	s.deletedMWD = append(s.deletedMWD, deletedMWDArgs{imsi, scAddr})
	// Remove matching record
	recs := s.mwds[imsi]
	filtered := recs[:0]
	for _, r := range recs {
		if r.SCAddress != scAddr {
			filtered = append(filtered, r)
		}
	}
	s.mwds[imsi] = filtered
	return nil
}

// ── stub implementations for the rest of the interface ───────────────────────

func (s *s6cStore) GetAUCByIMSI(_ context.Context, _ string) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetAUCByID(_ context.Context, _ int) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) AtomicGetAndIncrementSQN(_ context.Context, _ int, _ int64) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) ResyncSQN(_ context.Context, _ int, _ int64) error { return nil }
func (s *s6cStore) GetAlgorithmProfile(_ context.Context, _ int64) (*models.AlgorithmProfile, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetAPNByID(_ context.Context, _ int) (*models.APN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetAPNsByIDs(_ context.Context, _ []int) ([]models.APN, error) { return nil, nil }
func (s *s6cStore) UpdateServingMME(_ context.Context, _ string, _ *repository.ServingMMEUpdate) error {
	return nil
}
func (s *s6cStore) UpdateServingSGSN(_ context.Context, _ string, _ *repository.ServingSGSNUpdate) error {
	return nil
}
func (s *s6cStore) UpdateServingVLR(_ context.Context, _ string, _ *repository.ServingVLRUpdate) error {
	return nil
}
func (s *s6cStore) UpdateServingMSC(_ context.Context, _ string, _ *repository.ServingMSCUpdate) error {
	return nil
}
func (s *s6cStore) UpdateServingAMF(_ context.Context, _ string, _ *repository.ServingAMFUpdate) error {
	return nil
}
func (s *s6cStore) UpsertServingPDUSession(_ context.Context, _ *models.ServingPDUSession) error {
	return nil
}
func (s *s6cStore) DeleteServingPDUSession(_ context.Context, _ string, _ int) error { return nil }
func (s *s6cStore) ListServingPDUSessions(_ context.Context, _ string) ([]models.ServingPDUSession, error) {
	return nil, nil
}
func (s *s6cStore) GetIMSSubscriberByMSISDN(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetIMSSubscriberByIMSI(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) UpdateIMSSCSCF(_ context.Context, _ string, _ *repository.IMSSCSCFUpdate) error {
	return nil
}
func (s *s6cStore) UpdateIMSPCSCF(_ context.Context, _ string, _ *repository.IMSPCSCFUpdate) error {
	return nil
}
func (s *s6cStore) GetIFCProfileByID(_ context.Context, _ int) (*models.IFCProfile, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetAPNByName(_ context.Context, _ string) (*models.APN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetAllChargingRules(_ context.Context) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *s6cStore) GetChargingRulesByNames(_ context.Context, _ []string) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *s6cStore) GetChargingRulesByIDs(_ context.Context, _ []int) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *s6cStore) GetTFTsByGroupID(_ context.Context, _ int) ([]models.TFT, error) { return nil, nil }
func (s *s6cStore) UpsertServingAPN(_ context.Context, _ *models.ServingAPN) error  { return nil }
func (s *s6cStore) DeleteServingAPNBySession(_ context.Context, _ string) error     { return nil }
func (s *s6cStore) GetServingAPNBySession(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetServingAPNByIMSI(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetServingAPNByMSISDN(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetServingAPNByIdentity(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetServingAPNByUEIP(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *s6cStore) GetServingAPNByUEIPv6Prefix(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetSubscriberRoutingBySubscriberAndAPN(_ context.Context, _, _ int) (*models.SubscriberRouting, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) GetSubscriberRoutingsBySubscriberAndAPNs(_ context.Context, _ int, _ []int) ([]models.SubscriberRouting, error) {
	return nil, nil
}
func (s *s6cStore) GetRoamingRuleByMCCMNC(_ context.Context, _, _ string) (*models.RoamingRules, error) {
	return nil, repository.ErrNotFound
}
func (s *s6cStore) UpsertEmergencySubscriber(_ context.Context, _ *models.EmergencySubscriber) error {
	return nil
}
func (s *s6cStore) DeleteEmergencySubscriberByIMSI(_ context.Context, _ string) error { return nil }
func (s *s6cStore) ListEIR(_ context.Context, _ *[]models.EIR) error                  { return nil }
func (s *s6cStore) EIRNoMatchResponse() int                                           { return 0 }
func (s *s6cStore) UpsertIMSIIMEIHistory(_ context.Context, _, _, _, _ string, _ int) error {
	return nil
}
func (s *s6cStore) InvalidateCache(_ string)                           {}
func (s *s6cStore) InvalidateAPNCache(_ int)                           {}
func (s *s6cStore) ListAllAUC(_ context.Context) ([]models.AUC, error) { return nil, nil }
func (s *s6cStore) ListAllSubscribers(_ context.Context) ([]models.Subscriber, error) {
	return nil, nil
}
func (s *s6cStore) ListAllIMSSubscribers(_ context.Context) ([]models.IMSSubscriber, error) {
	return nil, nil
}
func (s *s6cStore) ListAllServingAPN(_ context.Context) ([]repository.GeoredServingAPN, error) {
	return nil, nil
}
func (s *s6cStore) UpsertSubscriber(_ context.Context, _ *models.Subscriber) error { return nil }
func (s *s6cStore) DeleteSubscriberByIMSI(_ context.Context, _ string) error       { return nil }
func (s *s6cStore) UpsertAUC(_ context.Context, _ *models.AUC) error               { return nil }
func (s *s6cStore) DeleteAUCByID(_ context.Context, _ int) error                   { return nil }
func (s *s6cStore) UpsertAPN(_ context.Context, _ *models.APN) error               { return nil }
func (s *s6cStore) DeleteAPNByID(_ context.Context, _ int) error                   { return nil }
func (s *s6cStore) UpsertIMSSubscriber(_ context.Context, _ *models.IMSSubscriber) error {
	return nil
}
func (s *s6cStore) DeleteIMSSubscriberByMSISDN(_ context.Context, _ string) error { return nil }
func (s *s6cStore) UpsertEIR(_ context.Context, _ *models.EIR) error              { return nil }
func (s *s6cStore) DeleteEIRByID(_ context.Context, _ int) error                  { return nil }

// ── request builder helpers ───────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

// buildSRISMByMSISDN builds a Send-Routing-Info-for-SM request addressed by MSISDN.
// The MSISDN is TBCD-encoded as required by TS 29.338 §7.3.2 / TS 23.003 §12.1.
func buildSRISMByMSISDN(t *testing.T, msisdn string) *diam.Message {
	t.Helper()
	req := diam.NewRequest(cmdSRISM, AppIDS6c, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("smsc.test;1;srism"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("smsc.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP,
		datatype.OctetString(encodeMSISDNBytes(msisdn)))
	return req
}

// buildSRISMByIMSI builds an SRI-SM request addressed by IMSI (User-Name).
func buildSRISMByIMSI(t *testing.T, imsi string) *diam.Message {
	t.Helper()
	req := diam.NewRequest(cmdSRISM, AppIDS6c, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("smsc.test;2;srism"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("smsc.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avp.UserName, avp.Mbit, 0, datatype.UTF8String(imsi))
	return req
}

// buildRDSMByMSISDN builds a Report-SM-Delivery-Status request.
// outcomeAVP may be nil (absent outcome) or a pre-built SM-Delivery-Outcome grouped AVP.
func buildRDSMByMSISDN(t *testing.T, msisdn, scAddr string, outcomeAVP *diam.AVP) *diam.Message {
	return buildRDSMByMSISDNWithFlags(t, msisdn, scAddr, outcomeAVP, 0)
}

func buildRDSMByMSISDNWithFlags(t *testing.T, msisdn, scAddr string, outcomeAVP *diam.AVP, rdrFlags uint32) *diam.Message {
	t.Helper()
	req := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("smsc.test;3;rdsm"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("smsc.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP,
		datatype.OctetString(encodeMSISDNBytes(msisdn)))
	req.NewAVP(avpSCAddress, avp.Mbit|avp.Vbit, Vendor3GPP,
		datatype.OctetString(encodeMSISDNBytes(scAddr)))
	if rdrFlags != 0 {
		req.NewAVP(avpRDRFlags, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(rdrFlags))
	}
	if outcomeAVP != nil {
		req.InsertAVP(outcomeAVP)
	}
	return req
}

// buildRDSMByIMSI builds an RDSM request addressed by IMSI.
func buildRDSMByIMSI(t *testing.T, imsi, scAddr string, outcomeAVP *diam.AVP) *diam.Message {
	t.Helper()
	req := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("smsc.test;4;rdsm"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("smsc.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avp.UserName, avp.Mbit, 0, datatype.UTF8String(imsi))
	req.NewAVP(avpSCAddress, avp.Mbit|avp.Vbit, Vendor3GPP,
		datatype.OctetString(encodeMSISDNBytes(scAddr)))
	if outcomeAVP != nil {
		req.InsertAVP(outcomeAVP)
	}
	return req
}

func buildUserIdentifierAVPForTest(imsi string, msisdn *string) *diam.AVP {
	children := []*diam.AVP{}
	if imsi != "" {
		children = append(children, diam.NewAVP(avp.UserName, avp.Mbit, 0, datatype.UTF8String(imsi)))
	}
	if msisdn != nil {
		children = append(children, diam.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP,
			datatype.OctetString(encodeMSISDNBytes(*msisdn))))
	}
	return diam.NewAVP(avpUserIdentifier, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: children})
}

// mmeOutcomeAVP builds an SM-Delivery-Outcome containing an MME-Delivery-Outcome.
func mmeOutcomeAVP(cause int32) *diam.AVP {
	return mmeOutcomeWithDiagnosticAVP(cause, nil)
}

func mmeOutcomeWithDiagnosticAVP(cause int32, diag *uint32) *diam.AVP {
	children := []*diam.AVP{
		diam.NewAVP(avpSMDeliveryCause, avp.Mbit|avp.Vbit, Vendor3GPP,
			datatype.Enumerated(cause)),
	}
	if diag != nil {
		children = append(children,
			diam.NewAVP(avpAbsentUserDiagnosticSM, avp.Mbit|avp.Vbit, Vendor3GPP,
				datatype.Unsigned32(*diag)),
		)
	}
	return diam.NewAVP(avpSMDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpMMEDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: children}),
		}})
}

func smsmiCorrelationIDAVP() *diam.AVP {
	return diam.NewAVP(avpSMSMICorrelationID, avp.Vbit, Vendor3GPP, datatype.OctetString([]byte{
		0x00, 0x00, 0x0c, 0xfd, 0x80, 0x00, 0x00, 0x14, 0x00, 0x00, 0x28, 0xaf,
		'h', 's', 's', '1',
	}))
}

// sgsnOutcomeAVP builds an SM-Delivery-Outcome containing an SGSN-Delivery-Outcome.
func sgsnOutcomeAVP(cause int32) *diam.AVP {
	return diam.NewAVP(avpSMDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpSGSNDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: []*diam.AVP{
					diam.NewAVP(avpSMDeliveryCause, avp.Mbit|avp.Vbit, Vendor3GPP,
						datatype.Enumerated(cause)),
				}}),
		}})
}

// ── response assertion helpers ────────────────────────────────────────────────
//
// These helpers scan msg.AVP directly rather than using msg.FindAVP, which
// requires the AVP name to be resolvable via the go-diameter dict from the
// message's ApplicationID context. Direct scanning avoids that limitation and
// is safe for vendor-specific AVPs registered under a different application.

// findAVPDirect scans msg.AVP for the first AVP with the given code and
// vendorID. Use vendorID=0 for base (non-vendor) AVPs.
func findAVPDirect(msg *diam.Message, code, vendorID uint32) *diam.AVP {
	for _, a := range msg.AVP {
		if a.Code == code && a.VendorID == vendorID {
			return a
		}
	}
	return nil
}

// requireResultCode asserts that the message carries Result-Code = code.
func requireResultCode(t *testing.T, msg *diam.Message, code uint32) {
	t.Helper()
	a := findAVPDirect(msg, avp.ResultCode, 0)
	if a == nil {
		t.Fatal("missing Result-Code AVP")
	}
	got := uint32(a.Data.(datatype.Unsigned32))
	if got != code {
		t.Errorf("Result-Code: got %d, want %d", got, code)
	}
}

// requireExperimentalResultCode asserts Experimental-Result-Code = code.
func requireExperimentalResultCode(t *testing.T, msg *diam.Message, code uint32) {
	t.Helper()
	erAVP := findAVPDirect(msg, avp.ExperimentalResult, 0)
	if erAVP == nil {
		t.Fatal("missing Experimental-Result AVP")
	}
	grp, ok := erAVP.Data.(*diam.GroupedAVP)
	if !ok {
		t.Fatal("Experimental-Result is not a grouped AVP")
	}
	for _, a := range grp.AVP {
		if a.Code == avp.ExperimentalResultCode {
			got := uint32(a.Data.(datatype.Unsigned32))
			if got != code {
				t.Errorf("Experimental-Result-Code: got %d, want %d", got, code)
			}
			return
		}
	}
	t.Fatal("Experimental-Result-Code AVP not found inside Experimental-Result")
}

// requireUserName asserts that User-Name = imsi.
func requireUserName(t *testing.T, msg *diam.Message, imsi string) {
	t.Helper()
	a := findAVPDirect(msg, avp.UserName, 0)
	if a == nil {
		t.Fatal("missing User-Name AVP")
	}
	got := string(a.Data.(datatype.UTF8String))
	if got != imsi {
		t.Errorf("User-Name: got %q, want %q", got, imsi)
	}
}

// requireMSISDN asserts that the MSISDN AVP (code 701, vendor 10415) decodes to want.
func requireMSISDN(t *testing.T, msg *diam.Message, want string) {
	t.Helper()
	a := findAVPDirect(msg, avpMSISDN, Vendor3GPP)
	if a == nil {
		t.Fatal("missing MSISDN AVP")
	}
	got := decodeMSISDN(a.Data.(datatype.OctetString))
	if got != want {
		t.Errorf("MSISDN: got %q, want %q", got, want)
	}
}

func requireUserIdentifier(t *testing.T, msg *diam.Message, wantIMSI string, wantMSISDN *string) {
	t.Helper()
	a := findAVPDirect(msg, avpUserIdentifier, Vendor3GPP)
	if a == nil {
		t.Fatal("missing User-Identifier AVP")
	}
	group, ok := a.Data.(*diam.GroupedAVP)
	if !ok {
		t.Fatalf("User-Identifier type = %T, want *diam.GroupedAVP", a.Data)
	}
	if userNameAVP := findGroupedChildAVP(group, avp.UserName, 0); userNameAVP == nil {
		t.Fatal("User-Identifier missing User-Name child")
	} else if got := string(userNameAVP.Data.(datatype.UTF8String)); got != wantIMSI {
		t.Fatalf("User-Identifier User-Name: got %q, want %q", got, wantIMSI)
	}
	if wantMSISDN == nil {
		return
	}
	msisdnAVP := findGroupedChildAVP(group, avpMSISDN, Vendor3GPP)
	if msisdnAVP == nil {
		t.Fatal("User-Identifier missing MSISDN child")
	}
	if got := decodeMSISDN(msisdnAVP.Data.(datatype.OctetString)); got != *wantMSISDN {
		t.Fatalf("User-Identifier MSISDN: got %q, want %q", got, *wantMSISDN)
	}
}

// requireServingNodeMMEName asserts Serving-Node contains MME-Name = mme.
func requireServingNodeMMEName(t *testing.T, msg *diam.Message, mme string) {
	t.Helper()
	nodeAVP := findAVPDirect(msg, avpServingNode, Vendor3GPP)
	if nodeAVP == nil {
		t.Fatal("missing Serving-Node AVP")
	}
	grp, ok := nodeAVP.Data.(*diam.GroupedAVP)
	if !ok {
		t.Fatal("Serving-Node is not a grouped AVP")
	}
	for _, a := range grp.AVP {
		if a.Code == avpMMEName {
			got := string(a.Data.(datatype.DiameterIdentity))
			if got != mme {
				t.Errorf("MME-Name: got %q, want %q", got, mme)
			}
			return
		}
	}
	t.Fatal("MME-Name AVP not found inside Serving-Node")
}

func requireServingNodeMMENumberForMTSMS(t *testing.T, msg *diam.Message, want string) {
	t.Helper()
	nodeAVP := findAVPDirect(msg, avpServingNode, Vendor3GPP)
	if nodeAVP == nil {
		t.Fatal("missing Serving-Node AVP")
	}
	grp, ok := nodeAVP.Data.(*diam.GroupedAVP)
	if !ok {
		t.Fatal("Serving-Node is not a grouped AVP")
	}
	for _, a := range grp.AVP {
		if a.Code == avpMMENumberForMTSMS {
			got := decodeMSISDN(a.Data.(datatype.OctetString))
			if got != want {
				t.Errorf("MME-Number-for-MT-SMS: got %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatal("MME-Number-for-MT-SMS AVP not found inside Serving-Node")
}

// requireMWDStatus asserts the MWD-Status AVP equals want.
func requireMWDStatus(t *testing.T, msg *diam.Message, want uint32) {
	t.Helper()
	a := findAVPDirect(msg, avpMWDStatus, Vendor3GPP)
	if a == nil {
		t.Fatalf("missing MWD-Status AVP")
	}
	got := uint32(a.Data.(datatype.Unsigned32))
	if got != want {
		t.Errorf("MWD-Status: got 0x%x, want 0x%x", got, want)
	}
}

// requireNoAVP asserts the AVP with given code and vendorID is absent from the message.
func requireNoAVP(t *testing.T, msg *diam.Message, code uint32, vendorID uint32) {
	t.Helper()
	if findAVPDirect(msg, code, vendorID) != nil {
		t.Errorf("AVP code=%d vendor=%d present but should be absent", code, vendorID)
	}
}

// ── TBCD encoding tests (TS 23.003 §12.1) ────────────────────────────────────

// TestTBCDEvenLength verifies TBCD packing for an even-length digit string.
// TS 23.003 §12.1: digits are packed two per byte, first digit in low nibble.
func TestTBCDEvenLength(t *testing.T) {
	// "3312" → bytes: [0x31, 0x21] (swap nibbles: 3↔3, 1↔2 → 0x31=0011_0001, 0x21=0010_0001)
	// digit '3'=0x3, '3'=0x3 → byte = (3<<4)|3 = 0x33
	// digit '1'=0x1, '2'=0x2 → byte = (2<<4)|1 = 0x21
	in := "3312"
	b := encodeMSISDNBytes(in)
	if len(b) != 2 {
		t.Fatalf("even 4-digit: want 2 bytes, got %d", len(b))
	}
	if b[0] != 0x33 || b[1] != 0x21 {
		t.Errorf("even 4-digit: got [% x], want [33 21]", b)
	}
	// Round-trip
	got := decodeMSISDN(datatype.OctetString(b))
	if got != in {
		t.Errorf("round-trip even: got %q, want %q", got, in)
	}
}

// TestTBCDOddLength verifies that an odd number of digits is padded with 0xF in
// the high nibble of the last byte. TS 23.003 §12.1.
func TestTBCDOddLength(t *testing.T) {
	// "331" → pad to "331F" → [0x33, 0xF1]
	in := "331"
	b := encodeMSISDNBytes(in)
	if len(b) != 2 {
		t.Fatalf("odd 3-digit: want 2 bytes, got %d", len(b))
	}
	// lo nibble of byte[1] = digit '1' = 0x1; hi nibble = 0xF
	if b[1] != 0xF1 {
		t.Errorf("odd 3-digit: byte[1] got 0x%02x, want 0xF1", b[1])
	}
	// Round-trip strips the 0xF pad
	got := decodeMSISDN(datatype.OctetString(b))
	if got != in {
		t.Errorf("round-trip odd: got %q, want %q", got, in)
	}
}

// TestTBCDRoundTripE164 verifies round-trip for a full E.164 MSISDN.
func TestTBCDRoundTripE164(t *testing.T) {
	cases := []string{
		"33612345678",  // 11 digits (odd) — typical French MSISDN
		"447700900000", // 12 digits (even) — UK mobile
		"12125551234",  // 11 digits — US number
		"1",            // minimal single digit
	}
	for _, msisdn := range cases {
		encoded := encodeMSISDNBytes(msisdn)
		decoded := decodeMSISDN(datatype.OctetString(encoded))
		if decoded != msisdn {
			t.Errorf("round-trip %q: got %q", msisdn, decoded)
		}
	}
}

// ── parseDeliveryOutcome tests ────────────────────────────────────────────────

// TestParseDeliveryOutcomeMME verifies extraction from MME-Delivery-Outcome sub-AVP.
// TS 29.338 §7.3.16 — SM-Delivery-Outcome contains node-specific sub-grouped AVPs.
func TestParseDeliveryOutcomeMME(t *testing.T) {
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	msg.InsertAVP(mmeOutcomeAVP(SMDeliveryCauseAbsentUser))

	result := parseDeliveryOutcome(msg)
	if result.Cause != SMDeliveryCauseAbsentUser {
		t.Errorf("MME outcome cause: got %d, want %d", result.Cause, SMDeliveryCauseAbsentUser)
	}
}

// TestParseDeliveryOutcomeSGSN verifies extraction from SGSN-Delivery-Outcome sub-AVP.
func TestParseDeliveryOutcomeSGSN(t *testing.T) {
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	msg.InsertAVP(sgsnOutcomeAVP(SMDeliveryCauseMemoryCapacityExceeded))

	result := parseDeliveryOutcome(msg)
	if result.Cause != SMDeliveryCauseMemoryCapacityExceeded {
		t.Errorf("SGSN outcome cause: got %d, want %d", result.Cause, SMDeliveryCauseMemoryCapacityExceeded)
	}
}

// TestParseDeliveryOutcomeMSC verifies MSC-Delivery-Outcome (code 3319).
// TS 29.338 §7.3.18 — MSC-Delivery-Outcome for GERAN/UTRAN access.
func TestParseDeliveryOutcomeMSC(t *testing.T) {
	const avpMSCDeliveryOutcome = uint32(3319)
	outcome := diam.NewAVP(avpSMDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpMSCDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: []*diam.AVP{
					diam.NewAVP(avpSMDeliveryCause, avp.Mbit|avp.Vbit, Vendor3GPP,
						datatype.Enumerated(SMDeliveryCauseSuccessfulTransfer)),
				}}),
		}})
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	msg.InsertAVP(outcome)

	result := parseDeliveryOutcome(msg)
	if result.Cause != SMDeliveryCauseSuccessfulTransfer {
		t.Errorf("MSC outcome cause: got %d, want %d", result.Cause, SMDeliveryCauseSuccessfulTransfer)
	}
}

// TestParseDeliveryOutcomeIPSMGW verifies IP-SM-GW-Delivery-Outcome (code 3320).
// TS 29.338 §7.3.19 — used by IP-SM-GW nodes.
func TestParseDeliveryOutcomeIPSMGW(t *testing.T) {
	const avpIPSMGWDeliveryOutcome = uint32(3320)
	outcome := diam.NewAVP(avpSMDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpIPSMGWDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: []*diam.AVP{
					diam.NewAVP(avpSMDeliveryCause, avp.Mbit|avp.Vbit, Vendor3GPP,
						datatype.Enumerated(SMDeliveryCauseAbsentUser)),
				}}),
		}})
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	msg.InsertAVP(outcome)

	result := parseDeliveryOutcome(msg)
	if result.Cause != SMDeliveryCauseAbsentUser {
		t.Errorf("IP-SM-GW outcome cause: got %d, want %d", result.Cause, SMDeliveryCauseAbsentUser)
	}
}

// TestParseDeliveryOutcomeAbsent verifies that a missing SM-Delivery-Outcome
// returns Cause = -1. TS 29.338 §5.3.2.4: absent outcome → treat as AbsentUser.
func TestParseDeliveryOutcomeAbsent(t *testing.T) {
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	// No SM-Delivery-Outcome AVP added.
	result := parseDeliveryOutcome(msg)
	if result.Cause != -1 {
		t.Errorf("absent outcome: got Cause %d, want -1", result.Cause)
	}
}

// TestParseDeliveryOutcomeAbsentUserDiagnostic verifies that the
// Absent-User-Diagnostic-SM code is extracted when present.
// TS 29.338 §7.3.22 — diagnostic code is optional within the node sub-AVP.
func TestParseDeliveryOutcomeAbsentUserDiagnostic(t *testing.T) {
	const diagCode = uint32(42)
	outcome := diam.NewAVP(avpSMDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpMMEDeliveryOutcome, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: []*diam.AVP{
					diam.NewAVP(avpSMDeliveryCause, avp.Mbit|avp.Vbit, Vendor3GPP,
						datatype.Enumerated(SMDeliveryCauseAbsentUser)),
					diam.NewAVP(avpAbsentUserDiagnosticSM, avp.Mbit|avp.Vbit, Vendor3GPP,
						datatype.Unsigned32(diagCode)),
				}}),
		}})
	msg := diam.NewRequest(cmdRDSM, AppIDS6c, dict.Default)
	msg.InsertAVP(outcome)

	result := parseDeliveryOutcome(msg)
	if result.Cause != SMDeliveryCauseAbsentUser {
		t.Errorf("cause: got %d, want %d", result.Cause, SMDeliveryCauseAbsentUser)
	}
	if result.AbsentUserDiagnostic != diagCode {
		t.Errorf("diagnostic: got %d, want %d", result.AbsentUserDiagnostic, diagCode)
	}
}

// ── SRI-SM tests (TS 29.338 §5.3.2.1 / §5.3.2.2) ────────────────────────────

// TestSRISM_AttachedSubscriber verifies normal SRI-SM response for an attached UE.
//
// TS 29.338 §5.3.2.1: When the subscriber is attached to an MME, the HSS shall
// return Result-Code SUCCESS and populate Serving-Node with MME-Name.
// The IMSI shall be returned in User-Name and the MSISDN in the MSISDN AVP.
func TestSRISM_AttachedSubscriber(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612345678"
	mmeName := "mme1.epc.mnc001.mcc001.3gppnetwork.org"
	store.addSubscriber(&models.Subscriber{
		IMSI:                "001010000000001",
		MSISDN:              ptr(msisdn),
		ServingMME:          ptr(mmeName),
		MMERegisteredForSMS: ptr(true),
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByMSISDN(t, msisdn))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	// TS 29.338 §6.1: answer must carry Result-Code = 2001 (SUCCESS)
	requireResultCode(t, ans, 2001)

	// TS 29.338 §5.3.2.1: User-Name (IMSI) must be present in the answer
	requireUserName(t, ans, "001010000000001")

	// TS 23.003 §12.1: MSISDN must be TBCD-encoded and returned
	requireMSISDN(t, ans, msisdn)

	// TS 29.338 §5.3.2.1: Serving-Node with MME-Name must be present
	requireServingNodeMMEName(t, ans, mmeName)

	// TS 29.338 §5.3.2.1: MWD-Status must NOT be present when subscriber is attached
	requireNoAVP(t, ans, avpMWDStatus, Vendor3GPP)
}

// TestSRISM_AbsentSubscriberMNRF verifies SRI-SM response for an unattached UE.
//
// TS 29.338 §5.3.2.2: When the subscriber has no serving node (not attached),
// the HSS shall set the MNRF bit (0x02) in MWD-Status and omit Serving-Node.
func TestSRISM_AbsentSubscriberMNRF(t *testing.T) {
	store := newS6cStore()
	msisdn := "447700900001"
	store.addSubscriber(&models.Subscriber{
		IMSI:       "001010000000002",
		MSISDN:     ptr(msisdn),
		ServingMME: nil, // not attached
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByMSISDN(t, msisdn))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserName(t, ans, "001010000000002")

	// TS 29.338 §5.3.2.2: MNRF bit (0x02) must be set
	requireMWDStatus(t, ans, MWDStatusMNRF)

	// TS 29.338 §5.3.2.2: Serving-Node must be absent when UE is not attached
	requireNoAVP(t, ans, avpServingNode, Vendor3GPP)
}

// TestSRISM_LookupByIMSI verifies that the HSS can resolve a subscriber when
// the SMS-SC sends the User-Name (IMSI) instead of MSISDN.
// TS 29.338 §6.3 (Send-Routing-Info-for-SM-Request) — both fields are optional
// but at least one shall be present; IMSI takes precedence.
func TestSRISM_LookupByIMSI(t *testing.T) {
	store := newS6cStore()
	imsi := "001010000000003"
	mmeName := "mme2.epc.mnc001.mcc001.3gppnetwork.org"
	store.addSubscriber(&models.Subscriber{
		IMSI:                imsi,
		MSISDN:              ptr("33698765432"),
		ServingMME:          ptr(mmeName),
		MMERegisteredForSMS: ptr(true),
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByIMSI(t, imsi))
	if err != nil {
		t.Fatalf("SRISR (by IMSI) returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserName(t, ans, imsi)
	requireServingNodeMMEName(t, ans, mmeName)
}

// TestSRISM_UnknownSubscriber verifies that a lookup for an unknown MSISDN
// returns Experimental-Result-Code 5001 (DIAMETER_ERROR_USER_UNKNOWN).
// TS 29.338 §6.3 / TS 29.272 §7.4.2 — Error-Message: "User-Unknown"
func TestSRISM_UnknownSubscriber(t *testing.T) {
	store := newS6cStore() // empty — no subscribers

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByMSISDN(t, "33600000000"))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	// TS 29.272 §7.4.2.1: error answer carries Experimental-Result-Code 5001
	requireExperimentalResultCode(t, ans, 5001)
}

// TestSRISM_NoMSISDNInResponse verifies that when a subscriber has no MSISDN
// configured the answer omits the MSISDN AVP (optional per TS 29.338 §6.3).
func TestSRISM_NoMSISDNInResponse(t *testing.T) {
	store := newS6cStore()
	imsi := "001010000000004"
	store.addSubscriber(&models.Subscriber{
		IMSI:                imsi,
		MSISDN:              nil, // no MSISDN provisioned
		ServingMME:          ptr("mme3.epc.mnc001.mcc001.3gppnetwork.org"),
		MMERegisteredForSMS: ptr(true),
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByIMSI(t, imsi))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireNoAVP(t, ans, avpMSISDN, Vendor3GPP)
}

// TestSRISM_ServingMMERealm verifies that MME-Realm is included in Serving-Node
// when the subscriber record has a realm stored.
// TS 29.338 §7.3.8 (Serving-Node) — MME-Realm is optional but should be returned
// if known.
func TestSRISM_ServingMMERealm(t *testing.T) {
	store := newS6cStore()
	msisdn := "33699001122"
	mmeName := "mme4.epc.mnc001.mcc001.3gppnetwork.org"
	mmeRealm := "epc.mnc001.mcc001.3gppnetwork.org"
	mmeNumberForMTSMS := "33611223344"
	store.addSubscriber(&models.Subscriber{
		IMSI:                "001010000000005",
		MSISDN:              ptr(msisdn),
		ServingMME:          ptr(mmeName),
		ServingMMERealm:     ptr(mmeRealm),
		MMENumberForMTSMS:   ptr(mmeNumberForMTSMS),
		MMERegisteredForSMS: ptr(true),
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByMSISDN(t, msisdn))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireServingNodeMMENumberForMTSMS(t, ans, mmeNumberForMTSMS)

	// Verify that the MME-Realm AVP appears inside Serving-Node.
	nodeAVP := findAVPDirect(ans, avpServingNode, Vendor3GPP)
	if nodeAVP == nil {
		t.Fatal("Serving-Node AVP missing")
	}
	grp := nodeAVP.Data.(*diam.GroupedAVP)
	found := false
	for _, a := range grp.AVP {
		if a.Code == avpMMERealm {
			got := string(a.Data.(datatype.DiameterIdentity))
			if got != mmeRealm {
				t.Errorf("MME-Realm: got %q, want %q", got, mmeRealm)
			}
			found = true
		}
	}
	if !found {
		t.Error("MME-Realm AVP not found inside Serving-Node")
	}
}

func TestSRISM_AttachedWithoutSMSRegistrationReturnsMNRF(t *testing.T) {
	store := newS6cStore()
	msisdn := "33644556677"
	store.addSubscriber(&models.Subscriber{
		IMSI:                "001010000000006",
		MSISDN:              ptr(msisdn),
		ServingMME:          ptr("mme5.epc.mnc001.mcc001.3gppnetwork.org"),
		MMERegisteredForSMS: ptr(false),
	})

	h := newTestHandlers(store)
	ans, err := h.SRISR(nil, buildSRISMByMSISDN(t, msisdn))
	if err != nil {
		t.Fatalf("SRISR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserName(t, ans, "001010000000006")
	requireMWDStatus(t, ans, MWDStatusMNRF)
	requireNoAVP(t, ans, avpServingNode, Vendor3GPP)
}

// ── RSDS tests (TS 29.338 §5.3.2.4) ─────────────────────────────────────────

// TestRSDS_SuccessfulTransfer verifies that a SuccessfulTransfer delivery report
// causes the HSS to delete any existing MWD and return Result-Code 2001.
//
// TS 29.338 §5.3.2.4: "If the SM-Delivery-Cause is 'successful transfer', the HSS
// shall delete the MWD record … and shall return Result-Code SUCCESS."
func TestRSDS_SuccessfulTransfer(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000001"
	scAddr := "33600000001"
	imsi := "001010000000010"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseSuccessfulTransfer)))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)

	// TS 29.338 §5.3.2.4: MWD-Status must NOT be present on successful transfer
	requireNoAVP(t, ans, avpMWDStatus, Vendor3GPP)

	// Verify that DeleteMWD was called for the correct IMSI / SC-Address
	if len(store.deletedMWD) != 1 {
		t.Fatalf("expected 1 DeleteMWD call, got %d", len(store.deletedMWD))
	}
	if store.deletedMWD[0].imsi != imsi {
		t.Errorf("DeleteMWD imsi: got %q, want %q", store.deletedMWD[0].imsi, imsi)
	}
	if store.deletedMWD[0].scAddr != scAddr {
		t.Errorf("DeleteMWD scAddr: got %q, want %q", store.deletedMWD[0].scAddr, scAddr)
	}
	// StoreMWD must NOT have been called
	if len(store.storedMWD) != 0 {
		t.Errorf("StoreMWD called %d times; want 0 for SuccessfulTransfer", len(store.storedMWD))
	}
}

// TestRSDS_AbsentUser verifies that an AbsentUser delivery failure causes the
// HSS to store MWD with MNRF flag and return MWD-Status = MNRF.
//
// TS 29.338 §5.3.2.4: "If SM-Delivery-Cause is 'absent user', the HSS shall
// store the MWD … set the MNRF flag … and include MWD-Status in the answer."
func TestRSDS_AbsentUser(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000002"
	scAddr := "33600000002"
	imsi := "001010000000011"
	diagCode := uint32(77)
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeWithDiagnosticAVP(SMDeliveryCauseAbsentUser, &diagCode)))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserIdentifier(t, ans, imsi, ptr(msisdn))

	// TS 29.338 §5.3.2.4 / §7.3.12: MNRF bit (0x02) must be set in MWD-Status
	requireMWDStatus(t, ans, MWDStatusMNRF)

	// Verify StoreMWD was called with MNRF status flag
	if len(store.storedMWD) != 1 {
		t.Fatalf("expected 1 StoreMWD call, got %d", len(store.storedMWD))
	}
	if store.storedMWD[0].statusFlags != MWDStatusMNRF {
		t.Errorf("StoreMWD statusFlags: got 0x%x, want 0x%x",
			store.storedMWD[0].statusFlags, MWDStatusMNRF)
	}
	if store.storedMWD[0].imsi != imsi {
		t.Errorf("StoreMWD imsi: got %q, want %q", store.storedMWD[0].imsi, imsi)
	}
	if store.storedMWD[0].absentUserDiagnosticSM == nil || *store.storedMWD[0].absentUserDiagnosticSM != diagCode {
		t.Fatalf("StoreMWD absent_user_diagnostic_sm = %+v, want %d", store.storedMWD[0].absentUserDiagnosticSM, diagCode)
	}
}

// TestRSDS_MemoryCapacityExceeded verifies MCEF path.
//
// TS 29.338 §5.3.2.4: "If SM-Delivery-Cause is 'UE memory capacity exceeded',
// the HSS shall store the MWD … set the MCEF flag."
func TestRSDS_MemoryCapacityExceeded(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000003"
	scAddr := "33600000003"
	imsi := "001010000000012"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseMemoryCapacityExceeded)))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserIdentifier(t, ans, imsi, ptr(msisdn))

	// TS 29.338 §7.3.12: MCEF bit (0x04) must be set
	requireMWDStatus(t, ans, MWDStatusMCEF)

	if len(store.storedMWD) != 1 {
		t.Fatalf("expected 1 StoreMWD call, got %d", len(store.storedMWD))
	}
	if store.storedMWD[0].statusFlags != MWDStatusMCEF {
		t.Errorf("StoreMWD statusFlags: got 0x%x, want 0x%x (MCEF)",
			store.storedMWD[0].statusFlags, MWDStatusMCEF)
	}
}

// TestRSDS_AbsentOutcome verifies that a missing SM-Delivery-Outcome defaults
// to AbsentUser/MNRF per TS 29.338 §5.3.2.4.
func TestRSDS_AbsentOutcome(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000004"
	scAddr := "33600000004"
	imsi := "001010000000013"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	// Pass nil → no SM-Delivery-Outcome AVP in the request
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr, nil))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserIdentifier(t, ans, imsi, ptr(msisdn))
	// TS 29.338 §5.3.2.4: absent outcome → treat as AbsentUser → MNRF
	requireMWDStatus(t, ans, MWDStatusMNRF)

	if len(store.storedMWD) != 1 || store.storedMWD[0].statusFlags != MWDStatusMNRF {
		t.Errorf("absent outcome: expected MNRF MWD stored, got storedMWD=%v", store.storedMWD)
	}
}

// TestRSDS_SingleAttemptDeliverySkipsMWDStore verifies that the HSS does not
// add MWD retry state when the Single-Attempt-Delivery bit is set in RDR-Flags.
func TestRSDS_SingleAttemptDeliverySkipsMWDStore(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000008"
	scAddr := "33600000008"
	imsi := "001010000000017"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDNWithFlags(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseAbsentUser), RDRFlagsSingleAttemptDelivery))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireNoAVP(t, ans, avpMWDStatus, Vendor3GPP)

	if len(store.storedMWD) != 0 {
		t.Fatalf("StoreMWD called %d times, want 0 for Single-Attempt-Delivery", len(store.storedMWD))
	}
	if len(store.mwds[imsi]) != 0 {
		t.Fatalf("MWD records remaining = %d, want 0 for Single-Attempt-Delivery", len(store.mwds[imsi]))
	}
}

func TestRSDS_MismatchedMSISDNReturnsStoredMSISDN(t *testing.T) {
	store := newS6cStore()
	storedMSISDN := "33612000009"
	requestMSISDN := "33612000999"
	scAddr := "33600000009"
	imsi := "001010000000018"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(storedMSISDN)})

	h := newTestHandlers(store)
	req := buildRDSMByIMSI(t, imsi, scAddr, mmeOutcomeAVP(SMDeliveryCauseAbsentUser))
	req.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP,
		datatype.OctetString(encodeMSISDNBytes(requestMSISDN)))
	ans, err := h.RDSMR(nil, req)
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireUserIdentifier(t, ans, imsi, ptr(storedMSISDN))
}

func TestRSDS_MWDListFullReturnsExperimentalResult(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000010"
	scAddr := "33600000999"
	imsi := "001010000000019"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})
	for i := 0; i < maxMWDEntriesPerSubscriber; i++ {
		store.mwds[imsi] = append(store.mwds[imsi], models.MessageWaitingData{
			IMSI:      imsi,
			SCAddress: fmt.Sprintf("33600000%03d", i),
		})
	}

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseAbsentUser)))
	if err != nil {
		t.Fatalf("RDSMR returned unexpected error: %v", err)
	}

	requireExperimentalResultCode(t, ans, 5558)
	if len(store.storedMWD) != 0 {
		t.Fatalf("StoreMWD called %d times, want 0 when MWD list is full", len(store.storedMWD))
	}
}

func TestRSDS_StoresSMSMICorrelationID(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000011"
	scAddr := "33600000011"
	imsi := "001010000000020"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	req := buildRDSMByMSISDN(t, msisdn, scAddr, mmeOutcomeAVP(SMDeliveryCauseAbsentUser))
	req.InsertAVP(smsmiCorrelationIDAVP())

	h := newTestHandlers(store)
	_, err := h.RDSMR(nil, req)
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	if len(store.storedMWD) != 1 {
		t.Fatalf("expected 1 StoreMWD call, got %d", len(store.storedMWD))
	}
	want := base64.StdEncoding.EncodeToString([]byte{
		0x00, 0x00, 0x0c, 0xfd, 0x80, 0x00, 0x00, 0x14, 0x00, 0x00, 0x28, 0xaf,
		'h', 's', 's', '1',
	})
	if store.storedMWD[0].smsmiCorrelationID == nil || *store.storedMWD[0].smsmiCorrelationID != want {
		t.Fatalf("StoreMWD smsmi_correlation_id = %+v, want %q", store.storedMWD[0].smsmiCorrelationID, want)
	}
}

func TestRSDS_LookupByUserIdentifierIMSI(t *testing.T) {
	store := newS6cStore()
	imsi := "001010000000021"
	scAddr := "33600000012"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr("33612000012")})

	req := buildRDSMByMSISDN(t, "33699999999", scAddr, mmeOutcomeAVP(SMDeliveryCauseAbsentUser))
	req.InsertAVP(buildUserIdentifierAVPForTest(imsi, nil))

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, req)
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	if len(store.storedMWD) != 1 || store.storedMWD[0].imsi != imsi {
		t.Fatalf("storedMWD = %+v, want one record for imsi=%q", store.storedMWD, imsi)
	}
}

func TestRSDS_LookupByUserIdentifierMSISDN(t *testing.T) {
	store := newS6cStore()
	imsi := "001010000000022"
	msisdn := "33612000013"
	scAddr := "33600000013"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	req := buildRDSMByIMSI(t, "001010009999999", scAddr, mmeOutcomeAVP(SMDeliveryCauseAbsentUser))
	req.InsertAVP(buildUserIdentifierAVPForTest("", &msisdn))

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, req)
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	if len(store.storedMWD) != 1 || store.storedMWD[0].imsi != imsi {
		t.Fatalf("storedMWD = %+v, want one record for imsi=%q", store.storedMWD, imsi)
	}
}

// TestRSDS_UnknownSubscriber verifies Experimental-Result-Code 5001 for an
// unknown subscriber in an RSDS request.
func TestRSDS_UnknownSubscriber(t *testing.T) {
	store := newS6cStore()

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, "33699999999", "33600000005", nil))
	if err == nil {
		t.Fatal("expected error for unknown subscriber, got nil")
	}

	requireExperimentalResultCode(t, ans, 5001)

	// No MWD should be stored for an unknown subscriber
	if len(store.storedMWD) != 0 {
		t.Errorf("StoreMWD called for unknown subscriber")
	}
}

// TestRSDS_LookupByIMSI verifies that RSDS can resolve a subscriber when
// identified by User-Name (IMSI) rather than MSISDN.
func TestRSDS_LookupByIMSI(t *testing.T) {
	store := newS6cStore()
	imsi := "001010000000014"
	scAddr := "33600000006"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr("33612000005")})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByIMSI(t, imsi, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseAbsentUser)))
	if err != nil {
		t.Fatalf("RDSMR (by IMSI) returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireMWDStatus(t, ans, MWDStatusMNRF)

	if len(store.storedMWD) == 0 {
		t.Fatal("StoreMWD not called for IMSI-addressed RSDS")
	}
	if store.storedMWD[0].imsi != imsi {
		t.Errorf("StoreMWD imsi: got %q, want %q", store.storedMWD[0].imsi, imsi)
	}
}

// TestRSDS_SGSNDeliveryOutcome verifies that an SGSN delivery outcome (used for
// GPRS/UTRAN access) is handled identically to MME delivery outcome.
// TS 29.338 §7.3.17 — SGSN-Delivery-Outcome mirrors MME-Delivery-Outcome structure.
func TestRSDS_SGSNDeliveryOutcome(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000006"
	scAddr := "33600000007"
	imsi := "001010000000015"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	ans, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		sgsnOutcomeAVP(SMDeliveryCauseAbsentUser)))
	if err != nil {
		t.Fatalf("RDSMR (SGSN outcome) returned error: %v", err)
	}

	requireResultCode(t, ans, 2001)
	requireMWDStatus(t, ans, MWDStatusMNRF)
}

// TestRSDS_SCAddressMWDRecord verifies that the SC-Address in the stored MWD
// record matches the decoded (plain-digit) SC-Address from the RSDS request.
// This is important for ALSC: the HSS must send ALSC to the correct SC-Address.
func TestRSDS_SCAddressMWDRecord(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000007"
	scAddr := "33699112233"
	imsi := "001010000000016"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	_, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseAbsentUser)))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	if len(store.storedMWD) != 1 {
		t.Fatalf("expected 1 stored MWD, got %d", len(store.storedMWD))
	}
	// The SC-Address must be stored as a plain-digit string (TBCD decoded).
	if store.storedMWD[0].scAddr != scAddr {
		t.Errorf("stored SC-Address: got %q, want %q", store.storedMWD[0].scAddr, scAddr)
	}
}

// TestRSDS_OriginHostAndRealmStoredInMWD verifies that Origin-Host and
// Origin-Realm from the RSDS request are persisted into the MWD record.
// The HSS needs these to address the subsequent ALSC request back to the SMS-SC.
// TS 29.338 §5.3.2.4 — the HSS shall record the address of the SMS-SC.
func TestRSDS_OriginHostAndRealmStoredInMWD(t *testing.T) {
	store := newS6cStore()
	msisdn := "33612000008"
	scAddr := "33699000001"
	imsi := "001010000000017"
	store.addSubscriber(&models.Subscriber{IMSI: imsi, MSISDN: ptr(msisdn)})

	h := newTestHandlers(store)
	_, err := h.RDSMR(nil, buildRDSMByMSISDN(t, msisdn, scAddr,
		mmeOutcomeAVP(SMDeliveryCauseAbsentUser)))
	if err != nil {
		t.Fatalf("RDSMR returned error: %v", err)
	}

	if len(store.storedMWD) != 1 {
		t.Fatalf("expected 1 stored MWD, got %d", len(store.storedMWD))
	}
	rec := store.storedMWD[0]
	if rec.scOriginHost != "smsc.test.net" {
		t.Errorf("scOriginHost: got %q, want %q", rec.scOriginHost, "smsc.test.net")
	}
	if rec.scOriginRealm != "test.net" {
		t.Errorf("scOriginRealm: got %q, want %q", rec.scOriginRealm, "test.net")
	}
}
