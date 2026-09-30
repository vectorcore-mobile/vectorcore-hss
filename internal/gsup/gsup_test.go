package gsup

// Integration test for the GSUP/IPA server.
//
// The test spins up a real TCP listener using a mock repository populated with
// a single subscriber (TS 35.207 Test Set 1 Ki/OPc) and exercises the full
// IPA handshake + SendAuthInfo request/response path.
//
// No external database is required -- the mock satisfies the repository.Repository
// interface with in-memory data.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// ── mock repository ───────────────────────────────────────────────────────────

// mockStore implements repository.Repository for tests.
// Only the methods exercised by GSUP handlers (AIR, ULR, PUR) are populated;
// the rest return ErrNotFound.
type mockStore struct {
	auc *models.AUC
	sub *models.Subscriber
}

func (m *mockStore) GetAUCByIMSI(_ context.Context, imsi string) (*models.AUC, error) {
	if m.auc != nil && m.auc.IMSI != nil && *m.auc.IMSI == imsi {
		return m.auc, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetAUCByID(_ context.Context, id int) (*models.AUC, error) {
	if m.auc != nil && m.auc.AUCID == id {
		return m.auc, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockStore) AtomicGetAndIncrementSQN(_ context.Context, aucID int, delta int64) (*models.AUC, error) {
	if m.auc != nil && m.auc.AUCID == aucID {
		before := *m.auc
		m.auc.SQN += delta
		return &before, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockStore) ResyncSQN(_ context.Context, aucID int, newSQN int64) error {
	if m.auc != nil && m.auc.AUCID == aucID {
		m.auc.SQN = newSQN
		return nil
	}
	return repository.ErrNotFound
}

func (m *mockStore) GetAlgorithmProfile(_ context.Context, _ int64) (*models.AlgorithmProfile, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetAPNByID(_ context.Context, _ int) (*models.APN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetAPNsByIDs(_ context.Context, _ []int) ([]models.APN, error) {
	return nil, nil
}

func (m *mockStore) GetSubscriberByIMSI(_ context.Context, imsi string) (*models.Subscriber, error) {
	if m.sub != nil && m.sub.IMSI == imsi {
		return m.sub, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetSubscriberByMSISDN(_ context.Context, _ string) (*models.Subscriber, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) UpdateServingMME(_ context.Context, _ string, _ *repository.ServingMMEUpdate) error {
	return nil
}

func (m *mockStore) UpdateServingSGSN(_ context.Context, _ string, _ *repository.ServingSGSNUpdate) error {
	return nil
}

func (m *mockStore) UpdateServingVLR(_ context.Context, _ string, _ *repository.ServingVLRUpdate) error {
	return nil
}
func (m *mockStore) UpdateServingMSC(_ context.Context, _ string, _ *repository.ServingMSCUpdate) error {
	return nil
}

func (m *mockStore) GetIMSSubscriberByMSISDN(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetIMSSubscriberByIMSI(_ context.Context, _ string) (*models.IMSSubscriber, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) UpdateIMSSCSCF(_ context.Context, _ string, _ *repository.IMSSCSCFUpdate) error {
	return nil
}

func (m *mockStore) UpdateIMSPCSCF(_ context.Context, _ string, _ *repository.IMSPCSCFUpdate) error {
	return nil
}

func (m *mockStore) GetIFCProfileByID(_ context.Context, _ int) (*models.IFCProfile, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetAPNByName(_ context.Context, _ string) (*models.APN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetAllChargingRules(_ context.Context) ([]models.ChargingRule, error) {
	return nil, nil
}

func (m *mockStore) GetChargingRulesByNames(_ context.Context, _ []string) ([]models.ChargingRule, error) {
	return nil, nil
}

func (m *mockStore) GetChargingRulesByIDs(_ context.Context, _ []int) ([]models.ChargingRule, error) {
	return nil, nil
}

func (m *mockStore) GetTFTsByGroupID(_ context.Context, _ int) ([]models.TFT, error) {
	return nil, nil
}

func (m *mockStore) UpsertServingAPN(_ context.Context, _ *models.ServingAPN) error {
	return nil
}

func (m *mockStore) DeleteServingAPNBySession(_ context.Context, _ string) error {
	return nil
}

func (m *mockStore) GetServingAPNBySession(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetServingAPNByIMSI(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetServingAPNByMSISDN(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetServingAPNByIdentity(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetServingAPNByUEIP(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetServingAPNByUEIPv6Prefix(_ context.Context, _ string) (*models.ServingAPN, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetSubscriberRoutingBySubscriberAndAPN(_ context.Context, _, _ int) (*models.SubscriberRouting, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) GetSubscriberRoutingsBySubscriberAndAPNs(_ context.Context, _ int, _ []int) ([]models.SubscriberRouting, error) {
	return nil, nil
}

func (m *mockStore) GetRoamingRuleByMCCMNC(_ context.Context, _, _ string) (*models.RoamingRules, error) {
	return nil, repository.ErrNotFound
}

func (m *mockStore) UpsertEmergencySubscriber(_ context.Context, _ *models.EmergencySubscriber) error {
	return nil
}

func (m *mockStore) DeleteEmergencySubscriberByIMSI(_ context.Context, _ string) error {
	return nil
}

func (m *mockStore) ListEIR(_ context.Context, _ *[]models.EIR) error {
	return nil
}

func (m *mockStore) EIRNoMatchResponse() int {
	return 0
}

func (m *mockStore) UpsertIMSIIMEIHistory(_ context.Context, _, _, _, _ string, _ int) error {
	return nil
}

func (m *mockStore) InvalidateCache(_ string) {}
func (m *mockStore) InvalidateAPNCache(_ int) {}

// GeoRed snapshot reads — not exercised by GSUP tests.
func (m *mockStore) ListAllAUC(_ context.Context) ([]models.AUC, error) { return nil, nil }
func (m *mockStore) ListAllSubscribers(_ context.Context) ([]models.Subscriber, error) {
	return nil, nil
}
func (m *mockStore) ListAllIMSSubscribers(_ context.Context) ([]models.IMSSubscriber, error) {
	return nil, nil
}
func (m *mockStore) ListAllServingAPN(_ context.Context) ([]repository.GeoredServingAPN, error) {
	return nil, nil
}

// GeoRed OAM apply — not exercised by GSUP tests.
func (m *mockStore) UpsertSubscriber(_ context.Context, _ *models.Subscriber) error       { return nil }
func (m *mockStore) DeleteSubscriberByIMSI(_ context.Context, _ string) error             { return nil }
func (m *mockStore) UpsertAUC(_ context.Context, _ *models.AUC) error                     { return nil }
func (m *mockStore) DeleteAUCByID(_ context.Context, _ int) error                         { return nil }
func (m *mockStore) UpsertAPN(_ context.Context, _ *models.APN) error                     { return nil }
func (m *mockStore) DeleteAPNByID(_ context.Context, _ int) error                         { return nil }
func (m *mockStore) UpsertIMSSubscriber(_ context.Context, _ *models.IMSSubscriber) error { return nil }
func (m *mockStore) DeleteIMSSubscriberByMSISDN(_ context.Context, _ string) error        { return nil }
func (m *mockStore) UpsertEIR(_ context.Context, _ *models.EIR) error                     { return nil }
func (m *mockStore) DeleteEIRByID(_ context.Context, _ int) error                         { return nil }

// UDM methods — not exercised by GSUP tests.
func (m *mockStore) UpdateServingAMF(_ context.Context, _ string, _ *repository.ServingAMFUpdate) error {
	return nil
}
func (m *mockStore) UpsertServingPDUSession(_ context.Context, _ *models.ServingPDUSession) error {
	return nil
}
func (m *mockStore) DeleteServingPDUSession(_ context.Context, _ string, _ int) error { return nil }
func (m *mockStore) ListServingPDUSessions(_ context.Context, _ string) ([]models.ServingPDUSession, error) {
	return nil, nil
}
func (m *mockStore) StoreMWD(_ context.Context, _ *models.MessageWaitingData) error {
	return nil
}
func (m *mockStore) GetMWDForIMSI(_ context.Context, _ string) ([]models.MessageWaitingData, error) {
	return nil, nil
}
func (m *mockStore) DeleteMWD(_ context.Context, _, _ string) error { return nil }

// ── IPA client helpers ────────────────────────────────────────────────────────

// clientReadIPA reads one IPA frame from conn.
func clientReadIPA(t *testing.T, conn net.Conn) (proto, ext byte, payload []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		t.Fatalf("read IPA header: %v", err)
	}
	length := binary.BigEndian.Uint16(hdr[0:2])
	proto = hdr[2]
	if length == 0 {
		return proto, 0, nil
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Fatalf("read IPA body: %v", err)
	}
	if proto == ipaProtoOSMO && len(body) > 0 {
		return proto, body[0], body[1:]
	}
	return proto, 0, body
}

// clientWriteIDResp sends an IPA CCM ID_RESP identifying the test peer.
func clientWriteIDResp(t *testing.T, conn net.Conn, name string) {
	t.Helper()
	nameBytes := []byte(name + "\x00")  // null-terminated string
	itemLen := byte(1 + len(nameBytes)) // tag byte counts in the length
	inner := []byte{ccmMsgIDResp, itemLen, ipaTagUnitName}
	inner = append(inner, nameBytes...)
	frame := make([]byte, 3+len(inner))
	binary.BigEndian.PutUint16(frame[0:2], uint16(len(inner)))
	frame[2] = ipaProtoCCM
	copy(frame[3:], inner)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write ID_RESP: %v", err)
	}
}

// clientWriteGSUP wraps a GSUP payload in an IPA OSMO/GSUP frame.
func clientWriteGSUP(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	totalBody := 1 + len(payload)
	frame := make([]byte, 3+totalBody)
	binary.BigEndian.PutUint16(frame[0:2], uint16(totalBody))
	frame[2] = ipaProtoOSMO
	frame[3] = ipaExtGSUP
	copy(frame[4:], payload)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write GSUP: %v", err)
	}
}

// ── test setup ────────────────────────────────────────────────────────────────

const testIMSI = "311435000000001"

// newTestServer starts a GSUP server on a random port backed by a mock store.
func newTestServer(t *testing.T) string {
	t.Helper()

	imsi := testIMSI
	store := &mockStore{
		auc: &models.AUC{
			AUCID: 1,
			Ki:    "465b5ce8b199b49faa5f0a2ee238a6bc",
			OPc:   "cd63cb71954a9f4e48a5994e37a02baf",
			AMF:   "8000",
			SQN:   0,
			IMSI:  &imsi,
		},
		sub: &models.Subscriber{
			IMSI: testIMSI,
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	log, _ := zap.NewDevelopment()
	srv := &Server{
		cfg:   config.GSUPConfig{Enabled: true, BindAddress: "127.0.0.1", BindPort: 0},
		store: store,
		log:   log,
		pub:   geored.NoopTypedPublisher{},
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleConn(conn)
		}
	}()

	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// doHandshake reads the server's ID_GET, replies with ID_RESP, then reads the ID_ACK.
func doHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	proto, _, payload := clientReadIPA(t, conn)
	if proto != ipaProtoCCM {
		t.Fatalf("expected CCM frame after connect, got proto=0x%02X", proto)
	}
	if len(payload) == 0 || payload[0] != ccmMsgIDGet {
		t.Fatalf("expected ID_GET (0x%02X), got 0x%02X", ccmMsgIDGet, payload[0])
	}
	clientWriteIDResp(t, conn, "osmomsc-test")
	// Server sends ID_ACK after receiving ID_RESP.
	proto, _, payload = clientReadIPA(t, conn)
	if proto != ipaProtoCCM {
		t.Fatalf("expected CCM ID_ACK, got proto=0x%02X", proto)
	}
	if len(payload) == 0 || payload[0] != ccmMsgIDACK {
		t.Fatalf("expected ID_ACK (0x%02X), got 0x%02X", ccmMsgIDACK, payload[0])
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestGSUP_SendAuthInfo(t *testing.T) {
	addr := newTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	doHandshake(t, conn)

	// Request 2 vectors.
	air := NewMsg(MsgSendAuthInfoReq).
		Add(IEIMSITag, encodeIMSI(testIMSI)).
		AddByte(IENumberOfRequestedVec, 2)
	clientWriteGSUP(t, conn, air.Bytes())

	proto, ext, payload := clientReadIPA(t, conn)
	if proto != ipaProtoOSMO || ext != ipaExtGSUP {
		t.Fatalf("expected GSUP frame, got proto=0x%02X ext=0x%02X", proto, ext)
	}

	resp, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != MsgSendAuthInfoRes {
		t.Fatalf("expected SendAuthInfoRes (0x%02X), got 0x%02X", MsgSendAuthInfoRes, resp.Type)
	}

	// IMSI IE present and correct.
	imsiIE := resp.Get(IEIMSITag)
	if imsiIE == nil {
		t.Fatal("response missing IMSI IE")
	}
	if got := decodeIMSI(imsiIE.Data); got != testIMSI {
		t.Errorf("IMSI: got %q, want %q", got, testIMSI)
	}

	// Exactly 2 auth tuples.
	tuples := resp.GetAll(IEAuthTupleTag)
	if len(tuples) != 2 {
		t.Fatalf("expected 2 auth tuples, got %d", len(tuples))
	}

	for i, tuple := range tuples {
		// Tuples are nested TLV without a leading message-type byte.
		// Prepend 0x00 so Decode() can parse them.
		inner, err := Decode(append([]byte{0x00}, tuple.Data...))
		if err != nil {
			t.Fatalf("tuple[%d] decode: %v", i, err)
		}

		type check struct {
			name string
			tag  byte
			size int
		}
		for _, c := range []check{
			{"RAND", IERANDTag, 16},
			{"SRES", IESRESTag, 4},
			{"KC", IEKcTag, 8},
			{"IK", IEIKTag, 16},
			{"CK", IECKTag, 16},
			{"AUTN", IEAUTNTag, 16},
			{"XRES", IEXRESTag, 8},
		} {
			ie := inner.Get(c.tag)
			if ie == nil {
				t.Errorf("tuple[%d] missing %s IE (tag 0x%02X)", i, c.name, c.tag)
				continue
			}
			if len(ie.Data) != c.size {
				t.Errorf("tuple[%d] %s: got %d bytes, want %d", i, c.name, len(ie.Data), c.size)
			}
		}

		// RAND must not be all-zeros.
		if randIE := inner.Get(IERANDTag); randIE != nil {
			allZero := true
			for _, b := range randIE.Data {
				if b != 0 {
					allZero = false
					break
				}
			}
			if allZero {
				t.Errorf("tuple[%d] RAND is all zeros", i)
			}
		}

		t.Logf("tuple[%d] RAND=%x SRES=%x KC=%x AUTN=%x",
			i,
			inner.Get(IERANDTag).Data,
			inner.Get(IESRESTag).Data,
			inner.Get(IEKcTag).Data,
			inner.Get(IEAUTNTag).Data,
		)
	}
}

func TestGSUP_SendAuthInfo_UnknownIMSI(t *testing.T) {
	addr := newTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	doHandshake(t, conn)

	air := NewMsg(MsgSendAuthInfoReq).
		Add(IEIMSITag, encodeIMSI("999999999999999")).
		AddByte(IENumberOfRequestedVec, 1)
	clientWriteGSUP(t, conn, air.Bytes())

	proto, ext, payload := clientReadIPA(t, conn)
	if proto != ipaProtoOSMO || ext != ipaExtGSUP {
		t.Fatalf("expected GSUP frame, got proto=0x%02X ext=0x%02X", proto, ext)
	}
	resp, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Type != MsgSendAuthInfoErr {
		t.Fatalf("expected SendAuthInfoErr (0x%02X), got 0x%02X", MsgSendAuthInfoErr, resp.Type)
	}
	causeIE := resp.Get(IECause)
	if causeIE == nil || len(causeIE.Data) == 0 {
		t.Fatal("missing Cause IE in error response")
	}
	if causeIE.Data[0] != CauseIMSIUnknown {
		t.Errorf("cause: got 0x%02X, want CauseIMSIUnknown (0x%02X)", causeIE.Data[0], CauseIMSIUnknown)
	}
	t.Logf("SendAuthInfoErr with cause=0x%02X OK", causeIE.Data[0])
}

func TestGSUP_UpdateLocation(t *testing.T) {
	addr := newTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	doHandshake(t, conn)

	ulr := NewMsg(MsgUpdateLocReq).Add(IEIMSITag, encodeIMSI(testIMSI))
	clientWriteGSUP(t, conn, ulr.Bytes())

	// First response: UpdateLocRes.
	proto, ext, payload := clientReadIPA(t, conn)
	if proto != ipaProtoOSMO || ext != ipaExtGSUP {
		t.Fatalf("expected GSUP ULA, got proto=0x%02X ext=0x%02X", proto, ext)
	}
	ula, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode ULA: %v", err)
	}
	if ula.Type != MsgUpdateLocRes {
		t.Fatalf("expected UpdateLocRes (0x%02X), got 0x%02X", MsgUpdateLocRes, ula.Type)
	}
	t.Logf("ULA OK")

	// Second message: InsertSubscriberData.
	proto, ext, payload = clientReadIPA(t, conn)
	if proto != ipaProtoOSMO || ext != ipaExtGSUP {
		t.Fatalf("expected GSUP ISD, got proto=0x%02X ext=0x%02X", proto, ext)
	}
	isd, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode ISD: %v", err)
	}
	if isd.Type != MsgInsertDataReq {
		t.Fatalf("expected InsertDataReq (0x%02X), got 0x%02X", MsgInsertDataReq, isd.Type)
	}
	t.Logf("ISD OK")
}

func TestGSUP_PurgeMS(t *testing.T) {
	addr := newTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	doHandshake(t, conn)

	pur := NewMsg(MsgPurgeMSReq).Add(IEIMSITag, encodeIMSI(testIMSI))
	clientWriteGSUP(t, conn, pur.Bytes())

	proto, ext, payload := clientReadIPA(t, conn)
	if proto != ipaProtoOSMO || ext != ipaExtGSUP {
		t.Fatalf("expected GSUP PUA, got proto=0x%02X ext=0x%02X", proto, ext)
	}
	resp, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode PUA: %v", err)
	}
	if resp.Type != MsgPurgeMSRes {
		t.Fatalf("expected PurgeMSRes (0x%02X), got 0x%02X", MsgPurgeMSRes, resp.Type)
	}
	t.Logf("PUA OK")
}

func TestGSUP_Ping(t *testing.T) {
	addr := newTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	doHandshake(t, conn)

	ping := []byte{0x00, 0x01, ipaProtoCCM, ccmMsgPING}
	if _, err := conn.Write(ping); err != nil {
		t.Fatalf("write PING: %v", err)
	}

	proto, _, payload := clientReadIPA(t, conn)
	if proto != ipaProtoCCM {
		t.Fatalf("expected CCM PONG, got proto=0x%02X", proto)
	}
	if len(payload) == 0 || payload[0] != ccmMsgPONG {
		t.Fatalf("expected PONG (0x%02X), got 0x%02X", ccmMsgPONG, payload[0])
	}
	t.Log("PONG OK")
}

func TestGSUP_IMSIEncodeDecode(t *testing.T) {
	cases := []string{
		"311435000000001",
		"001010000000001",
		"310260000000001",
		"99999999999999",  // even length (14 digits)
		"311435123456789", // odd length
	}
	for _, imsi := range cases {
		encoded := encodeIMSI(imsi)
		decoded := decodeIMSI(encoded)
		if decoded != imsi {
			t.Errorf("round-trip IMSI %q: got %q", imsi, decoded)
		}
	}
}

// TestGSUP_Derive2GKc verifies the 2G KC derivation.
// Using TS 35.207 Test Set 1 CK/IK:
//
//	KC = CK[0:8] ^ CK[8:16] ^ IK[0:8] ^ IK[8:16]
func TestGSUP_Derive2GKc(t *testing.T) {
	mustHex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatalf("hex.DecodeString(%q): %v", s, err)
		}
		return b
	}

	ck := mustHex("b40ba9a3c58b2a05bbf0d987b21bf8cb")
	ik := mustHex("f769bcd751044604127672711c6d3441")
	kc := derive2GKc(ck, ik)

	if len(kc) != 8 {
		t.Fatalf("KC length: got %d, want 8", len(kc))
	}

	// Verify against manually computed XOR.
	var want [8]byte
	for i := 0; i < 8; i++ {
		want[i] = ck[i] ^ ck[i+8] ^ ik[i] ^ ik[i+8]
	}
	for i, b := range kc {
		if b != want[i] {
			t.Errorf("KC[%d]: got 0x%02X, want 0x%02X", i, b, want[i])
		}
	}
	t.Logf("KC = %x", kc)
}
