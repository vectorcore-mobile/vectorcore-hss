package cx

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/ims"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"go.uber.org/zap"
)

// PeerLookup lets Cx send RTR to registered S-CSCFs.
type PeerLookup interface {
	GetConn(originHost string) (diam.Conn, bool)
}

type Handlers struct {
	store       repository.Repository
	log         *zap.Logger
	originHost  string
	originRealm string
	mcc         string
	mnc         string
	peers       PeerLookup
	scscfPool   []string      // S-CSCF URIs for first-registration assignment
	scscfRR     atomic.Uint64 // round-robin counter for pool selection
	pub         geored.TypedPublisher
}

// encodePLMN encodes MCC/MNC strings into the 3-byte BCD PLMN format used by 3GPP.
// e.g. MCC="311", MNC="435" → [0x13, 0xF1, 0x54]
func encodePLMN(mcc, mnc string) []byte {
	// Pad MNC to 3 digits if needed
	if len(mnc) == 2 {
		mnc = mnc + "F"
	}
	digits := mcc + mnc // 6 hex digits
	if len(digits) < 6 {
		return []byte{0x00, 0xF1, 0x10} // safe fallback
	}
	nibble := func(c byte) byte {
		if c >= '0' && c <= '9' {
			return c - '0'
		}
		return 0xF
	}
	b0 := (nibble(digits[1]) << 4) | nibble(digits[0]) // MCC digit 2 | MCC digit 1
	b1 := (nibble(digits[5]) << 4) | nibble(digits[2]) // MNC digit 3 | MCC digit 3
	b2 := (nibble(digits[4]) << 4) | nibble(digits[3]) // MNC digit 2 | MNC digit 1
	return []byte{b0, b1, b2}
}

// normalizeIMSI extracts a bare IMSI from a Cx private identity.
// "311435000070570@ims.mnc435.mcc311.3gppnetwork.org" → "311435000070570"
// "311435000070570" → "311435000070570"
func normalizeIMSI(privateID string) string {
	if at := strings.IndexByte(privateID, '@'); at > 0 {
		return privateID[:at]
	}
	return privateID
}

// normalizeMSISDN extracts a bare MSISDN from a Cx public identity.
// "sip:13135551234@ims.mnc435.mcc311.3gppnetwork.org" → "13135551234"
// "sip:+13135551234@ims.mnc435.mcc311.3gppnetwork.org;user=phone" → "13135551234"
// "tel:+13135551234;phone-context=ims.mnc435.mcc311.3gppnetwork.org" → "13135551234"
// "13135551234" → "13135551234"
//
// The "+" of a global tel/SIP number (RFC 3966) is URI syntax, not part of the
// MSISDN, which is stored as digits only (TS 23.003 §3.3, TBCD in TS 29.329
// §6.3.2).
func normalizeMSISDN(publicID string) string {
	s := strings.TrimPrefix(publicID, "sip:")
	s = strings.TrimPrefix(s, "tel:")
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	if semi := strings.IndexByte(s, ';'); semi >= 0 {
		s = s[:semi]
	}
	return strings.TrimPrefix(s, "+")
}

// imsIMSDomain formats the IMS home domain for this PLMN.
func imsIMSDomain(mcc, mnc string) string {
	return fmt.Sprintf("ims.mnc%s.mcc%s.3gppnetwork.org", ims.NormalizeMNC(mnc), mcc)
}

// buildCxAnswer builds a bare Cx answer frame matching the PyHSS AVP layout:
// Session-Id, Origin-Host, Origin-Realm, Auth-Session-State, Vendor-Specific-Application-Id.
// Callers must add their application-specific AVPs and then append Result-Code or
// Experimental-Result last.
func buildCxAnswer(req *diam.Message, sessionID datatype.UTF8String, originHost, originRealm string) *diam.Message {
	ans := diam.NewMessage(req.Header.CommandCode, req.Header.CommandFlags&^diam.RequestFlag, AppIDCx, req.Header.HopByHopID, req.Header.EndToEndID, req.Dictionary())
	ans.InsertAVP(diam.NewAVP(avp.SessionID, avp.Mbit, 0, sessionID))
	ans.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(originHost))
	ans.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(originRealm))
	ans.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1)) // NO_STATE_MAINTAINED
	ans.NewAVP(avp.VendorSpecificApplicationID, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avp.VendorID, avp.Mbit, 0, datatype.Unsigned32(Vendor3GPP)),
		diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(AppIDCx)),
	}})
	return ans
}

// pickSCSCF returns the next S-CSCF URI from the pool using round-robin,
// or "" if the pool is empty.
func (h *Handlers) pickSCSCF() string {
	if len(h.scscfPool) == 0 {
		return ""
	}
	idx := h.scscfRR.Add(1) - 1
	return h.scscfPool[idx%uint64(len(h.scscfPool))]
}

func NewHandlers(cfg *config.Config, store repository.Repository, log *zap.Logger, peers PeerLookup) *Handlers {
	return &Handlers{
		store:       store,
		log:         log,
		originHost:  cfg.HSS.OriginHost,
		originRealm: cfg.HSS.OriginRealm,
		mcc:         cfg.HSS.MCC,
		mnc:         cfg.HSS.MNC,
		peers:       peers,
		scscfPool:   cfg.HSS.SCSCFPool,
		pub:         geored.NoopTypedPublisher{},
	}
}

// WithGeored attaches a GeoRed publisher to the Cx handler.
func (h *Handlers) WithGeored(pub geored.TypedPublisher) *Handlers {
	h.pub = pub
	return h
}
