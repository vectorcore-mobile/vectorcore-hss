package sh

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"go.uber.org/zap"
	xcontext "golang.org/x/net/context"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

func TestMain(m *testing.M) {
	if err := LoadDict(); err != nil {
		panic("sh tests: load dict: " + err.Error())
	}
	os.Exit(m.Run())
}

func TestUDR_MSISDNWithAddressHeaderReturnsSubscriberData(t *testing.T) {
	store := newShStore()
	store.addIMSSubscriber(&models.IMSSubscriber{
		MSISDN: "12345678901",
		IMSI:   ptr("001010123456789"),
	})
	h := newShTestHandlers(store)

	req := buildUDRByMSISDN([]byte{0x07, 0x91, 0x21, 0x43, 0x65, 0x87, 0x09, 0xF1}, DataReferenceMSISDN)
	ans, err := h.UDR(nil, req)
	if err != nil {
		t.Fatalf("UDR returned error: %v", err)
	}

	requireResultCode(t, ans, diam.Success)
	if got := store.msisdnLookups; len(got) != 1 || got[0] != "12345678901" {
		t.Fatalf("MSISDN lookups = %v, want [12345678901]", got)
	}

	userDataAVP := findAVPDirect(ans, avpUserData, Vendor3GPP)
	if userDataAVP == nil {
		t.Fatal("missing User-Data AVP")
	}
	userData := string(userDataAVP.Data.(datatype.OctetString))
	if !strings.Contains(userData, "<MSISDN>12345678901</MSISDN>") {
		t.Fatalf("User-Data missing normalized MSISDN, got %q", userData)
	}
}

func TestUDR_UnsupportedDataReferenceReturns5009(t *testing.T) {
	store := newShStore()
	store.addIMSSubscriber(&models.IMSSubscriber{
		MSISDN: "12345678901",
		IMSI:   ptr("001010123456789"),
	})
	h := newShTestHandlers(store)

	req := buildUDRByPublicIdentity("tel:12345678901", 999)
	ans, err := h.UDR(nil, req)
	if err != nil {
		t.Fatalf("UDR returned error: %v", err)
	}

	requireExperimentalResultCode(t, ans, DiameterErrorNotSupportedUserData)
	if a := findAVPDirect(ans, avpUserData, Vendor3GPP); a != nil {
		t.Fatal("unexpected User-Data AVP on unsupported Data-Reference")
	}
}

func TestSendPNRStoresPendingSessionUntilPNA(t *testing.T) {
	conn := &captureConn{}
	peers := &peerLookupStub{conns: map[string]diam.Conn{"as1.test.net": conn}}
	h := newShTestHandlers(newShStore())
	h.peers = peers

	h.SendPNR("sip:12345678901@test.net", "as1.test.net", "test.net", "<Sh-Data/>")

	msg := parseWrittenMessage(t, conn)
	if msg.Header.CommandCode != cmdPNR {
		t.Fatalf("Command-Code: got %d, want %d", msg.Header.CommandCode, cmdPNR)
	}
	sidAVP := findAVPDirect(msg, avp.SessionID, 0)
	if sidAVP == nil {
		t.Fatal("missing Session-Id AVP")
	}
	sessionID := string(sidAVP.Data.(datatype.UTF8String))
	if _, ok := h.pendingPNR.Load(sessionID); !ok {
		t.Fatal("pending PNR session not recorded")
	}

	h.PNA(nil, buildPNA(sessionID, diam.Success))

	if _, ok := h.pendingPNR.Load(sessionID); ok {
		t.Fatal("pending PNR session still present after PNA")
	}
}

func newShTestHandlers(store repository.Repository) *Handlers {
	cfg := &config.Config{}
	cfg.HSS.OriginHost = "hss.test.net"
	cfg.HSS.OriginRealm = "test.net"
	cfg.HSS.MCC = "001"
	cfg.HSS.MNC = "01"
	return NewHandlers(cfg, store, zap.NewNop(), &noopPeerLookup{})
}

type noopPeerLookup struct{}

func (n *noopPeerLookup) GetConn(_ string) (diam.Conn, bool) { return nil, false }

type peerLookupStub struct {
	conns map[string]diam.Conn
}

func (p *peerLookupStub) GetConn(originHost string) (diam.Conn, bool) {
	conn, ok := p.conns[originHost]
	return conn, ok
}

type captureConn struct {
	buf      bytes.Buffer
	writeErr error
	ctx      xcontext.Context
}

func (c *captureConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.buf.Write(b)
}

func (c *captureConn) WriteStream(b []byte, _ uint) (int, error) { return c.Write(b) }
func (c *captureConn) Close()                                    {}
func (c *captureConn) LocalAddr() net.Addr                       { return testAddr("local") }
func (c *captureConn) RemoteAddr() net.Addr                      { return testAddr("remote") }
func (c *captureConn) TLS() *tls.ConnectionState                 { return nil }
func (c *captureConn) Dictionary() *dict.Parser                  { return dict.Default }
func (c *captureConn) Context() xcontext.Context {
	if c.ctx == nil {
		return xcontext.Background()
	}
	return c.ctx
}
func (c *captureConn) SetContext(ctx xcontext.Context) { c.ctx = ctx }
func (c *captureConn) Connection() net.Conn            { return nil }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func parseWrittenMessage(t *testing.T, conn *captureConn) *diam.Message {
	t.Helper()
	msg, err := diam.ReadMessage(bytes.NewReader(conn.buf.Bytes()), dict.Default)
	if err != nil {
		t.Fatalf("read written Diameter message: %v", err)
	}
	return msg
}

type shStore struct {
	byMSISDN      map[string]*models.IMSSubscriber
	byIMSI        map[string]*models.IMSSubscriber
	msisdnLookups []string
}

func newShStore() *shStore {
	return &shStore{
		byMSISDN: make(map[string]*models.IMSSubscriber),
		byIMSI:   make(map[string]*models.IMSSubscriber),
	}
}

func (s *shStore) addIMSSubscriber(sub *models.IMSSubscriber) {
	s.byMSISDN[sub.MSISDN] = sub
	if sub.IMSI != nil {
		s.byIMSI[*sub.IMSI] = sub
	}
}

func (s *shStore) GetIMSSubscriberByMSISDN(_ context.Context, msisdn string) (*models.IMSSubscriber, error) {
	s.msisdnLookups = append(s.msisdnLookups, msisdn)
	if sub, ok := s.byMSISDN[msisdn]; ok {
		return sub, nil
	}
	return nil, repository.ErrNotFound
}

func (s *shStore) GetIMSSubscriberByIMSI(_ context.Context, imsi string) (*models.IMSSubscriber, error) {
	if sub, ok := s.byIMSI[imsi]; ok {
		return sub, nil
	}
	return nil, repository.ErrNotFound
}

func (s *shStore) GetIFCProfileByID(_ context.Context, _ int) (*models.IFCProfile, error) {
	return nil, repository.ErrNotFound
}

func (s *shStore) GetAUCByIMSI(_ context.Context, _ string) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetAUCByID(_ context.Context, _ int) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) AtomicGetAndIncrementSQN(_ context.Context, _ int, _ int64) (*models.AUC, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) ResyncSQN(_ context.Context, _ int, _ int64) error { return nil }
func (s *shStore) GetAlgorithmProfile(_ context.Context, _ int64) (*models.AlgorithmProfile, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetAPNByID(_ context.Context, _ int) (*models.APN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetAPNsByIDs(_ context.Context, _ []int) ([]models.APN, error) { return nil, nil }
func (s *shStore) GetSubscriberByIMSI(_ context.Context, _ string) (*models.Subscriber, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetSubscriberByMSISDN(_ context.Context, _ string) (*models.Subscriber, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) UpdateServingMME(_ context.Context, _ string, _ *repository.ServingMMEUpdate) error {
	return nil
}
func (s *shStore) UpdateServingSGSN(_ context.Context, _ string, _ *repository.ServingSGSNUpdate) error {
	return nil
}
func (s *shStore) UpdateServingVLR(_ context.Context, _ string, _ *repository.ServingVLRUpdate) error {
	return nil
}
func (s *shStore) UpdateServingMSC(_ context.Context, _ string, _ *repository.ServingMSCUpdate) error {
	return nil
}
func (s *shStore) UpdateServingAMF(_ context.Context, _ string, _ *repository.ServingAMFUpdate) error {
	return nil
}
func (s *shStore) UpsertServingPDUSession(_ context.Context, _ *models.ServingPDUSession) error {
	return nil
}
func (s *shStore) DeleteServingPDUSession(_ context.Context, _ string, _ int) error { return nil }
func (s *shStore) ListServingPDUSessions(_ context.Context, _ string) ([]models.ServingPDUSession, error) {
	return nil, nil
}
func (s *shStore) UpdateIMSSCSCF(_ context.Context, _ string, _ *repository.IMSSCSCFUpdate) error {
	return nil
}
func (s *shStore) UpdateIMSPCSCF(_ context.Context, _ string, _ *repository.IMSPCSCFUpdate) error {
	return nil
}
func (s *shStore) GetAPNByName(_ context.Context, _ string) (*models.APN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetAllChargingRules(_ context.Context) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *shStore) GetChargingRulesByNames(_ context.Context, _ []string) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *shStore) GetChargingRulesByIDs(_ context.Context, _ []int) ([]models.ChargingRule, error) {
	return nil, nil
}
func (s *shStore) GetTFTsByGroupID(_ context.Context, _ int) ([]models.TFT, error) { return nil, nil }
func (s *shStore) UpsertServingAPN(_ context.Context, _ *models.ServingAPN) error  { return nil }
func (s *shStore) DeleteServingAPNBySession(_ context.Context, _ string) error     { return nil }
func (s *shStore) GetServingAPNBySession(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetServingAPNByIMSI(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetServingAPNByMSISDN(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetServingAPNByIdentity(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetServingAPNByUEIP(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (s *shStore) GetServingAPNByUEIPv6Prefix(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetSubscriberRoutingBySubscriberAndAPN(_ context.Context, _, _ int) (*models.SubscriberRouting, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) GetSubscriberRoutingsBySubscriberAndAPNs(_ context.Context, _ int, _ []int) ([]models.SubscriberRouting, error) {
	return nil, nil
}
func (s *shStore) GetRoamingRuleByMCCMNC(_ context.Context, _, _ string) (*models.RoamingRules, error) {
	return nil, repository.ErrNotFound
}
func (s *shStore) UpsertEmergencySubscriber(_ context.Context, _ *models.EmergencySubscriber) error {
	return nil
}
func (s *shStore) DeleteEmergencySubscriberByIMSI(_ context.Context, _ string) error { return nil }
func (s *shStore) ListEIR(_ context.Context, _ *[]models.EIR) error                  { return nil }
func (s *shStore) EIRNoMatchResponse() int                                           { return 0 }
func (s *shStore) UpsertIMSIIMEIHistory(_ context.Context, _, _, _, _ string, _ int) error {
	return nil
}
func (s *shStore) StoreMWD(_ context.Context, _ *models.MessageWaitingData) error {
	return nil
}
func (s *shStore) GetMWDForIMSI(_ context.Context, _ string) ([]models.MessageWaitingData, error) {
	return nil, nil
}
func (s *shStore) DeleteMWD(_ context.Context, _, _ string) error { return nil }
func (s *shStore) InvalidateCache(_ string)                       {}
func (s *shStore) InvalidateAPNCache(_ int)                       {}
func (s *shStore) ListAllAUC(_ context.Context) ([]models.AUC, error) {
	return nil, nil
}
func (s *shStore) ListAllSubscribers(_ context.Context) ([]models.Subscriber, error) {
	return nil, nil
}
func (s *shStore) ListAllIMSSubscribers(_ context.Context) ([]models.IMSSubscriber, error) {
	return nil, nil
}
func (s *shStore) ListAllServingAPN(_ context.Context) ([]repository.GeoredServingAPN, error) {
	return nil, nil
}
func (s *shStore) UpsertSubscriber(_ context.Context, _ *models.Subscriber) error { return nil }
func (s *shStore) DeleteSubscriberByIMSI(_ context.Context, _ string) error       { return nil }
func (s *shStore) UpsertAUC(_ context.Context, _ *models.AUC) error               { return nil }
func (s *shStore) DeleteAUCByID(_ context.Context, _ int) error                   { return nil }
func (s *shStore) UpsertAPN(_ context.Context, _ *models.APN) error               { return nil }
func (s *shStore) DeleteAPNByID(_ context.Context, _ int) error                   { return nil }
func (s *shStore) UpsertIMSSubscriber(_ context.Context, _ *models.IMSSubscriber) error {
	return nil
}
func (s *shStore) DeleteIMSSubscriberByMSISDN(_ context.Context, _ string) error { return nil }
func (s *shStore) UpsertEIR(_ context.Context, _ *models.EIR) error              { return nil }
func (s *shStore) DeleteEIRByID(_ context.Context, _ int) error                  { return nil }

func ptr[T any](v T) *T { return &v }

func buildUDRByPublicIdentity(identity string, dataReference int32) *diam.Message {
	req := diam.NewRequest(306, AppIDSh, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("as.test;1;udr"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("as.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avpUserIdentity, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avpPublicIdentity, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.UTF8String(identity)),
	}})
	req.NewAVP(avpDataReference, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(dataReference))
	return req
}

func buildUDRByMSISDN(msisdn []byte, dataReference int32) *diam.Message {
	req := diam.NewRequest(306, AppIDSh, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String("as.test;2;udr"))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("as.test.net"))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("hss.test.net"))
	req.NewAVP(avpUserIdentity, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(msisdn)),
	}})
	req.NewAVP(avpDataReference, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(dataReference))
	return req
}

func buildPNA(sessionID string, resultCode uint32) *diam.Message {
	msg := diam.NewRequest(cmdPNR, AppIDSh, dict.Default).Answer(resultCode)
	msg.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(sessionID))
	msg.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("as.test.net"))
	msg.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("test.net"))
	return msg
}

func findAVPDirect(msg *diam.Message, code, vendorID uint32) *diam.AVP {
	for _, a := range msg.AVP {
		if a.Code == code && a.VendorID == vendorID {
			return a
		}
	}
	return nil
}

func requireResultCode(t *testing.T, msg *diam.Message, code uint32) {
	t.Helper()
	a := findAVPDirect(msg, avp.ResultCode, 0)
	if a == nil {
		t.Fatal("missing Result-Code AVP")
	}
	got := uint32(a.Data.(datatype.Unsigned32))
	if got != code {
		t.Fatalf("Result-Code: got %d, want %d", got, code)
	}
}

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
		if a.Code != avp.ExperimentalResultCode {
			continue
		}
		got := uint32(a.Data.(datatype.Unsigned32))
		if got != code {
			t.Fatalf("Experimental-Result-Code: got %d, want %d", got, code)
		}
		return
	}
	t.Fatal("Experimental-Result-Code AVP not found")
}
