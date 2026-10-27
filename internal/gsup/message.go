package gsup

// GSUP (Generic Subscriber Update Protocol) message encoding/decoding.
//
// Reference: Osmocom GSUP protocol specification
// https://ftp.osmocom.org/docs/latest/osmocom-gsup.pdf
//
// Each GSUP message is a sequence of TLV (Tag-Length-Value) information elements.
// The first IE is always the message type (tag 0x01).

// Message type codes (libosmocore enum osmo_gsup_message_type)
const (
	MsgUpdateLocReq = 0x04 // ULR -- peer reports location update
	MsgUpdateLocErr = 0x05
	MsgUpdateLocRes = 0x06 // ULA

	MsgSendAuthInfoReq = 0x08 // AIR -- peer requests auth vectors
	MsgSendAuthInfoErr = 0x09
	MsgSendAuthInfoRes = 0x0A // AIA -- HSS sends auth vectors

	MsgAuthFailureReport = 0x0B // peer reports auth failure -- no response

	MsgPurgeMSReq = 0x0C // PUR
	MsgPurgeMSErr = 0x0D
	MsgPurgeMSRes = 0x0E

	MsgInsertDataReq = 0x10 // ISD -- HSS pushes subscriber data
	MsgInsertDataErr = 0x11
	MsgInsertDataRes = 0x12

	MsgDeleteDataReq = 0x14
	MsgDeleteDataErr = 0x15
	MsgDeleteDataRes = 0x16

	MsgLocationCancelReq = 0x1C // sent by HSS to cancel location (not sent yet)
	MsgLocationCancelErr = 0x1D
	MsgLocationCancelRes = 0x1E
)

// Information element tags (libosmocore enum osmo_gsup_iei)
const (
	IEIMSITag              = 0x01
	IECause                = 0x02
	IEAuthTupleTag         = 0x03 // grouped: contains RAND, SRES, KC / RAND, XRES, CK, IK, AUTN
	IEPDPInfoComplete      = 0x04
	IEPDPContextTag        = 0x05 // grouped: PDP/APN info
	IECancelType           = 0x06
	IEFreezePTMSI          = 0x07
	IEMSISDNTag            = 0x08
	IEHLRNumber            = 0x09
	IEMessageClass         = 0x0A
	IEPDPContextID         = 0x10
	IEPDPType              = 0x11 // PDP address / type
	IEAccessPointName      = 0x12
	IEPDPQoSProfile        = 0x13
	IEChargingChar         = 0x14
	IERANDTag              = 0x20 // 16 bytes
	IESRESTag              = 0x21 // 4 bytes (2G)
	IEKcTag                = 0x22 // 8 bytes (2G session key)
	IEIKTag                = 0x23 // 16 bytes (3G integrity key)
	IECKTag                = 0x24 // 16 bytes (3G cipher key)
	IEAUTNTag              = 0x25 // 16 bytes
	IEAUTSTag              = 0x26 // 14 bytes (resync token)
	IEXRESTag              = 0x27 // RES, 4-16 bytes
	IECNDomain             = 0x28 // 0x01=PS, 0x02=CS
	IESupportedRATTypes    = 0x29
	IECurrentRATType       = 0x2A
	IENumberOfRequestedVec = 0x52
)

// CN domain values for IECNDomain
const (
	CNDomainPS = 0x01
	CNDomainCS = 0x02
)

// RAT type values for IECurrentRATType (libosmocore enum osmo_rat_type)
const (
	RATGERANA    = 0x01
	RATUTRANIu   = 0x02
	RATEUTRANSGs = 0x03
)

// maxAuthVectors is OSMO_GSUP_MAX_NUM_AUTH_INFO.
const maxAuthVectors = 5

// GMM cause codes (3GPP TS 24.008 Table 10.5.147) used in error responses
const (
	CauseIMSIUnknown        = 0x02
	CauseIllegalMS          = 0x03
	CauseIMEINotAccepted    = 0x05
	CauseIllegalME          = 0x06
	CauseNetworkFailure     = 0x11
	CauseCongestion         = 0x16
	CauseServiceNotAllowed  = 0x20
)

// Msg is a decoded GSUP message.
type Msg struct {
	Type byte
	IEs  []IE
}

// IE is a single GSUP information element.
type IE struct {
	Tag  byte
	Data []byte
}

// Decode parses a raw GSUP payload into a Msg.
func Decode(b []byte) (*Msg, error) {
	if len(b) < 1 {
		return nil, ErrShortMessage
	}
	m := &Msg{Type: b[0]}
	i := 1
	for i < len(b) {
		if i+1 >= len(b) {
			return nil, ErrShortMessage
		}
		tag := b[i]
		l := int(b[i+1])
		i += 2
		if i+l > len(b) {
			return nil, ErrShortMessage
		}
		m.IEs = append(m.IEs, IE{Tag: tag, Data: b[i : i+l]})
		i += l
	}
	return m, nil
}

// Get returns the first IE with the given tag, or nil.
func (m *Msg) Get(tag byte) *IE {
	for i := range m.IEs {
		if m.IEs[i].Tag == tag {
			return &m.IEs[i]
		}
	}
	return nil
}

// GetAll returns all IEs with the given tag.
func (m *Msg) GetAll(tag byte) []IE {
	var out []IE
	for _, ie := range m.IEs {
		if ie.Tag == tag {
			out = append(out, ie)
		}
	}
	return out
}

// Builder constructs a GSUP message payload byte-by-byte.
type Builder struct {
	buf []byte
}

// NewMsg starts a new GSUP message with the given type byte.
func NewMsg(msgType byte) *Builder {
	return &Builder{buf: []byte{msgType}}
}

// Add appends a TLV information element.
func (b *Builder) Add(tag byte, data []byte) *Builder {
	b.buf = append(b.buf, tag, byte(len(data)))
	b.buf = append(b.buf, data...)
	return b
}

// AddByte appends a single-byte value IE.
func (b *Builder) AddByte(tag, val byte) *Builder {
	return b.Add(tag, []byte{val})
}

// Bytes returns the encoded message.
func (b *Builder) Bytes() []byte {
	return b.buf
}

// encodeIMSI encodes an IMSI string as BCD nibble pairs (TBCD format).
// Each byte holds two digits; the string length determines whether padding is needed.
func encodeIMSI(imsi string) []byte {
	out := make([]byte, (len(imsi)+1)/2)
	for i, c := range imsi {
		d := byte(c - '0')
		if i%2 == 0 {
			out[i/2] = d
		} else {
			out[i/2] |= d << 4
		}
	}
	if len(imsi)%2 != 0 {
		out[len(out)-1] |= 0xF0 // pad odd-length with 0xF
	}
	return out
}

// decodeIMSI decodes a TBCD-encoded IMSI byte slice back to a digit string.
func decodeIMSI(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, byt := range b {
		lo := byt & 0x0F
		hi := (byt >> 4) & 0x0F
		out = append(out, '0'+lo)
		if hi != 0x0F {
			out = append(out, '0'+hi)
		}
	}
	return string(out)
}

// ErrShortMessage is returned when a GSUP payload is truncated.
type errShort struct{}

func (e errShort) Error() string { return "gsup: message too short" }

var ErrShortMessage error = errShort{}
