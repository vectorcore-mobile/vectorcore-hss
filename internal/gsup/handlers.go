package gsup

// handlers.go -- GSUP message dispatch: SendAuthInfo, UpdateLocation, PurgeMS.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/crypto"
	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// handleMessage dispatches an inbound GSUP message.
func (s *Server) handleMessage(p *peer, msg *Msg) {
	switch msg.Type {
	case MsgSendAuthInfoReq:
		s.handleAIR(p, msg)
	case MsgUpdateLocReq:
		s.handleULR(p, msg)
	case MsgInsertDataRes:
		s.handleInsertDataRes(p, msg)
	case MsgInsertDataErr:
		s.handleInsertDataErr(p, msg)
	case MsgPurgeMSReq:
		s.handlePUR(p, msg)
	case MsgAuthFailureReport, MsgDeleteDataRes:
		// No response needed.
	default:
		s.log.Warn("gsup: unhandled message type",
			zap.String("peer", p.name),
			zap.String("type", fmt.Sprintf("0x%02X", msg.Type)),
		)
	}
}

// ── Send Auth Info (AIR) ──────────────────────────────────────────────────────

func (s *Server) handleAIR(p *peer, msg *Msg) {
	imsi, ok := extractIMSI(msg)
	if !ok {
		s.log.Warn("gsup: AIR missing IMSI", zap.String("peer", p.name))
		sendError(p, MsgSendAuthInfoErr, "", CauseNetworkFailure)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	auc, err := s.store.GetAUCByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		s.log.Warn("gsup: AIR unknown IMSI", zap.String("imsi", imsi), zap.String("peer", p.name))
		sendError(p, MsgSendAuthInfoErr, imsi, CauseIMSIUnknown)
		return
	}
	if err != nil {
		s.log.Error("gsup: AIR db error", zap.String("imsi", imsi), zap.Error(err))
		sendError(p, MsgSendAuthInfoErr, imsi, CauseNetworkFailure)
		return
	}

	profile, err := crypto.LoadProfile(ctx, s.store, auc.AlgorithmProfileID)
	if err != nil {
		s.log.Error("gsup: AIR profile load error", zap.String("imsi", imsi), zap.Error(err))
		sendError(p, MsgSendAuthInfoErr, imsi, CauseNetworkFailure)
		return
	}

	// Number of requested vectors (IE 0x29); default to 1.
	numVec := 1
	if ie := msg.Get(IENumberOfRequestedVec); ie != nil && len(ie.Data) > 0 {
		numVec = int(ie.Data[0])
		if numVec < 1 {
			numVec = 1
		}
		if numVec > 5 {
			numVec = 5
		}
	}

	resp := NewMsg(MsgSendAuthInfoRes).Add(IEIMSITag, encodeIMSI(imsi))

	// Generate numVec auth tuples. Each EAP-AKA vector carries RAND, XRES,
	// AUTN, CK, IK -- exactly what the GSUP 3G quintuplet needs. The 2G
	// triplet (SRES, KC) is derived from CK/IK per 3GPP TS 55.205.
	for i := 0; i < numVec; i++ {
		v, err := crypto.GenerateEAPAKAVector(auc, profile, s.store, ctx)
		if err != nil {
			s.log.Error("gsup: AIR vector generation failed", zap.String("imsi", imsi), zap.Error(err))
			sendError(p, MsgSendAuthInfoErr, imsi, CauseNetworkFailure)
			return
		}

		// 2G material for CSFB/GSM auth:
		//   SRES = XRES[0:4]
		//   KC   = CK[0:8] XOR CK[8:16] XOR IK[0:8] XOR IK[8:16]  (3GPP TS 55.205)
		sres := v.XRES[0:4]
		kc := derive2GKc(v.CK, v.IK)

		tuple := NewMsg(0x00). // Builder reused for nested TLV encoding
					Add(IERANDTag, v.RAND).
					Add(IESRESTag, sres).
					Add(IEKcTag, kc).
					Add(IEIKTag, v.IK).
					Add(IECKTag, v.CK).
					Add(IEAUTNTag, v.AUTN).
					Add(IEXRESTag, v.XRES)

		// Strip the leading type byte -- we only want the IEs inside the tuple.
		resp.Add(IEAuthTupleTag, tuple.Bytes()[1:])
	}

	s.pub.PublishSQNUpdate(auc.AUCID, auc.SQN+int64(numVec)*32)

	if err := p.writeGSUP(resp.Bytes()); err != nil {
		s.log.Error("gsup: AIR write failed", zap.String("imsi", imsi), zap.Error(err))
		return
	}
	s.log.Info("gsup: AIR success",
		zap.String("imsi", imsi),
		zap.String("peer", p.name),
		zap.Int("vectors", numVec),
	)
}

// derive2GKc derives the GSM session key KC from Milenage CK and IK.
// KC = CK[0:8] XOR CK[8:16] XOR IK[0:8] XOR IK[8:16]  (3GPP TS 55.205 A.4)
func derive2GKc(ck, ik []byte) []byte {
	kc := make([]byte, 8)
	for i := 0; i < 8; i++ {
		kc[i] = ck[i] ^ ck[i+8] ^ ik[i] ^ ik[i+8]
	}
	return kc
}

// ── Update Location (ULR) ─────────────────────────────────────────────────────
//
// Sequence per osmo-hlr's lu_fsm: ULR → ISD (MSISDN, CN domain) → peer's
// InsertDataResult → ULA. The ULA is held back until the peer has stored the
// subscriber data; otherwise OsmoMSC completes the LU without an MSISDN.

func (s *Server) handleULR(p *peer, msg *Msg) {
	imsi, ok := extractIMSI(msg)
	if !ok {
		s.log.Warn("gsup: ULR missing IMSI", zap.String("peer", p.name))
		sendError(p, MsgUpdateLocErr, "", CauseNetworkFailure)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		s.log.Warn("gsup: ULR unknown IMSI", zap.String("imsi", imsi), zap.String("peer", p.name))
		sendError(p, MsgUpdateLocErr, imsi, CauseIMSIUnknown)
		return
	}
	if err != nil {
		s.log.Error("gsup: ULR db error", zap.String("imsi", imsi), zap.Error(err))
		sendError(p, MsgUpdateLocErr, imsi, CauseNetworkFailure)
		return
	}
	if sub.Enabled != nil && !*sub.Enabled {
		s.log.Warn("gsup: ULR subscriber disabled", zap.String("imsi", imsi))
		sendError(p, MsgUpdateLocErr, imsi, CauseServiceNotAllowed)
		return
	}

	// Update serving VLR and MSC — in OsmoMSC these are co-located so the
	// peer name is the same identity for both.
	peerName := p.name
	ts := time.Now().UTC()
	_ = s.store.UpdateServingVLR(ctx, imsi, &repository.ServingVLRUpdate{
		ServingVLR: &peerName,
		Timestamp:  &ts,
	})
	_ = s.store.UpdateServingMSC(ctx, imsi, &repository.ServingMSCUpdate{
		ServingMSC: &peerName,
		Timestamp:  &ts,
	})
	s.pub.PublishServingVLR(geored.PayloadServingVLR{IMSI: imsi, ServingVLR: &peerName, Timestamp: &ts})
	s.pub.PublishServingMSC(geored.PayloadServingMSC{IMSI: imsi, ServingMSC: &peerName, Timestamp: &ts})

	cnDomain := byte(CNDomainCS) // OsmoMSC ULRs are CS; OsmoSGSN sends PS
	if ie := msg.Get(IECNDomain); ie != nil && len(ie.Data) > 0 {
		cnDomain = ie.Data[0]
	}
	msgClass := msg.Get(IEMessageClass)

	// Register the pending LU before sending the ISD so a fast
	// InsertDataResult always finds it.
	timeout := s.luTimeout
	if timeout <= 0 {
		timeout = defaultLUTimeout
	}
	p.startLU(imsi, timeout, func() {
		s.log.Warn("gsup: ULR timed out waiting for InsertDataResult",
			zap.String("imsi", imsi), zap.String("peer", peerName))
		sendError(p, MsgUpdateLocErr, imsi, CauseNetworkFailure)
	})

	if err := s.sendISD(p, sub, cnDomain, msgClass); err != nil {
		p.takeLU(imsi, nil)
		return
	}
	s.log.Debug("gsup: ULR ISD sent, awaiting InsertDataResult",
		zap.String("imsi", imsi), zap.String("peer", p.name))
}

// sendISD pushes subscriber data (MSISDN) to the peer via ISD.
// cnDomain must match the domain from the ULR (CNDomainCS or CNDomainPS).
// msgClass, when non-nil, is the ULR's Message-Class IE, echoed as osmo-hlr does.
func (s *Server) sendISD(p *peer, sub *models.Subscriber, cnDomain byte, msgClass *IE) error {
	isd := NewMsg(MsgInsertDataReq).
		Add(IEIMSITag, encodeIMSI(sub.IMSI)).
		AddByte(IECNDomain, cnDomain)

	if sub.MSISDN != nil && *sub.MSISDN != "" {
		if enc, err := encodeMSISDN(*sub.MSISDN); err != nil {
			s.log.Error("gsup: ISD MSISDN not encodable, sending ISD without it",
				zap.String("imsi", sub.IMSI), zap.String("msisdn", *sub.MSISDN), zap.Error(err))
		} else {
			isd.Add(IEMSISDNTag, enc)
		}
	}
	if msgClass != nil {
		isd.Add(IEMessageClass, msgClass.Data)
	}

	if err := p.writeGSUP(isd.Bytes()); err != nil {
		s.log.Error("gsup: ISD write failed",
			zap.String("imsi", sub.IMSI),
			zap.String("peer", p.name),
			zap.Error(err),
		)
		return err
	}
	return nil
}

// handleInsertDataRes completes a pending Update Location with the ULA.
func (s *Server) handleInsertDataRes(p *peer, msg *Msg) {
	imsi, ok := extractIMSI(msg)
	if !ok || !p.takeLU(imsi, nil) {
		// ISD result outside an LU (none are sent today) — nothing to answer.
		s.log.Debug("gsup: InsertDataResult without pending ULR", zap.String("peer", p.name))
		return
	}
	ula := NewMsg(MsgUpdateLocRes).Add(IEIMSITag, encodeIMSI(imsi))
	if ie := msg.Get(IEMessageClass); ie != nil {
		ula.Add(IEMessageClass, ie.Data)
	}
	if err := p.writeGSUP(ula.Bytes()); err != nil {
		s.log.Error("gsup: ULA write failed", zap.String("imsi", imsi), zap.Error(err))
		return
	}
	s.log.Info("gsup: ULR success",
		zap.String("imsi", imsi),
		zap.String("peer", p.name),
	)
}

// handleInsertDataErr fails a pending Update Location, as osmo-hlr does.
func (s *Server) handleInsertDataErr(p *peer, msg *Msg) {
	imsi, ok := extractIMSI(msg)
	if !ok || !p.takeLU(imsi, nil) {
		s.log.Debug("gsup: InsertDataError without pending ULR", zap.String("peer", p.name))
		return
	}
	s.log.Warn("gsup: ULR failed, peer rejected ISD", zap.String("imsi", imsi), zap.String("peer", p.name))
	sendError(p, MsgUpdateLocErr, imsi, CauseNetworkFailure)
}

// ── Purge MS (PUR) ────────────────────────────────────────────────────────────

func (s *Server) handlePUR(p *peer, msg *Msg) {
	imsi, ok := extractIMSI(msg)
	if !ok {
		s.log.Warn("gsup: PUR missing IMSI", zap.String("peer", p.name))
		sendError(p, MsgPurgeMSErr, "", CauseNetworkFailure)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = s.store.UpdateServingVLR(ctx, imsi, &repository.ServingVLRUpdate{
		ServingVLR: nil,
		Timestamp:  nil,
	})
	_ = s.store.UpdateServingMSC(ctx, imsi, &repository.ServingMSCUpdate{
		ServingMSC: nil,
		Timestamp:  nil,
	})

	resp := NewMsg(MsgPurgeMSRes).Add(IEIMSITag, encodeIMSI(imsi))
	if err := p.writeGSUP(resp.Bytes()); err != nil {
		s.log.Error("gsup: PUR write failed", zap.String("imsi", imsi), zap.Error(err))
		return
	}
	s.log.Info("gsup: PUR success", zap.String("imsi", imsi), zap.String("peer", p.name))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func extractIMSI(msg *Msg) (string, bool) {
	ie := msg.Get(IEIMSITag)
	if ie == nil || len(ie.Data) == 0 {
		return "", false
	}
	return decodeIMSI(ie.Data), true
}

func sendError(p *peer, msgType byte, imsi string, cause byte) {
	b := NewMsg(msgType)
	if imsi != "" {
		b.Add(IEIMSITag, encodeIMSI(imsi))
	}
	b.AddByte(IECause, cause)
	_ = p.writeGSUP(b.Bytes())
}

// encodeMSISDN encodes an MSISDN as osmo-hlr does (gsm48_encode_bcd_number
// with h_len 0): a length byte followed by BCD digit pairs, low nibble
// first, odd length padded with 0xF. There is no TON/NPI byte; OsmoMSC
// decodes it with gsm48_decode_bcd_number2(..., h_len=0).
func encodeMSISDN(msisdn string) ([]byte, error) {
	msisdn = strings.TrimPrefix(msisdn, "+")
	if msisdn == "" {
		return nil, fmt.Errorf("empty MSISDN")
	}
	for _, c := range msisdn {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("MSISDN %q contains a non-digit", msisdn)
		}
	}
	if len(msisdn)%2 != 0 {
		msisdn += "F"
	}
	result := make([]byte, 1+len(msisdn)/2)
	result[0] = byte(len(msisdn) / 2)
	for i := 0; i < len(msisdn); i += 2 {
		lo := nibble(msisdn[i])
		hi := nibble(msisdn[i+1])
		result[1+i/2] = (hi << 4) | lo
	}
	return result, nil
}

func nibble(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	return 0xF
}
