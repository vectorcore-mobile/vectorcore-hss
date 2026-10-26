package gsup

// IPA (IP.access) protocol framing used by Osmocom as the transport layer
// for GSUP over TCP.
//
// Wire format of each IPA message:
//
//	[2 bytes] payload length (big-endian, NOT including the 3-byte header)
//	[1 byte]  protocol ID
//	[N bytes] payload
//
// Protocol IDs (libosmocore include/osmocom/gsm/protocol/ipaccess.h):
//
//	0xFE  IPAC_PROTO_IPACCESS -- CCM: PING/PONG, ID_GET/ID_RESP/ID_ACK
//	0xEE  IPAC_PROTO_OSMO     -- Osmocom extensions; ext byte 0x05 = GSUP
//
// The ext byte immediately follows the protocol byte for OSMO messages,
// so the effective header for a GSUP message is 4 bytes:
//
//	len(2) | proto=0xEE | ext=0x05 | gsup-payload...

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	ipaProtoCCM  = 0xFE // IPAC_PROTO_IPACCESS: Common Control Messages
	ipaProtoOSMO = 0xEE // IPAC_PROTO_OSMO: Osmocom extensions (GSUP, CTRL, ...)
	ipaExtGSUP   = 0x05 // IPAC_PROTO_EXT_GSUP

	// CCM message types (proto=0xFE)
	ccmMsgPING   = 0x00
	ccmMsgPONG   = 0x01
	ccmMsgIDGet  = 0x04
	ccmMsgIDResp = 0x05
	ccmMsgIDACK  = 0x06

	// CCM identity tags (IPAC_IDTAG_*)
	ipaTagSerial    = 0x00
	ipaTagUnitName  = 0x01
	ipaTagLocation1 = 0x02
	ipaTagLocation2 = 0x03
	ipaTagEquipVer  = 0x04
	ipaTagSwVer     = 0x05
	ipaTagIPAddr    = 0x06
	ipaTagMACAddr   = 0x07
	ipaTagUnitID    = 0x08
)

// ipaIDGetFrame is libosmocore's ipa_id_req_msg: an ID_GET requesting the
// same eight identity tags osmo-hlr asks a GSUP client for.
var ipaIDGetFrame = []byte{
	0x00, 0x11, ipaProtoCCM, ccmMsgIDGet,
	0x01, ipaTagUnitID,
	0x01, ipaTagMACAddr,
	0x01, ipaTagLocation1,
	0x01, ipaTagLocation2,
	0x01, ipaTagEquipVer,
	0x01, ipaTagSwVer,
	0x01, ipaTagUnitName,
	0x01, ipaTagSerial,
}

// ipaMsg is a decoded IPA frame.
type ipaMsg struct {
	proto   byte
	ext     byte // only valid when proto == ipaProtoOSMO
	payload []byte
}

// ipaRead reads and decodes one IPA frame from conn.
func ipaRead(conn net.Conn) (*ipaMsg, error) {
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint16(hdr[0:2])
	proto := hdr[2]

	if length == 0 {
		return &ipaMsg{proto: proto}, nil
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}

	msg := &ipaMsg{proto: proto}
	if proto == ipaProtoOSMO {
		msg.ext = body[0]
		msg.payload = body[1:]
	} else {
		msg.payload = body
	}
	return msg, nil
}

// ipaWriteGSUP writes a GSUP payload wrapped in IPA OSMO/GSUP framing.
func ipaWriteGSUP(w io.Writer, payload []byte) error {
	// Length field covers the ext byte + payload.
	totalBody := 1 + len(payload)
	if totalBody > 0xFFFF {
		return fmt.Errorf("ipa: GSUP payload too long (%d bytes)", len(payload))
	}
	frame := make([]byte, 3+totalBody)
	binary.BigEndian.PutUint16(frame[0:2], uint16(totalBody))
	frame[2] = ipaProtoOSMO
	frame[3] = ipaExtGSUP
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}

// ipaWriteCCM writes a payload-less CCM message (PONG, ID_ACK).
func ipaWriteCCM(w io.Writer, msgType byte) error {
	_, err := w.Write([]byte{0x00, 0x01, ipaProtoCCM, msgType})
	return err
}

// parseIDResp extracts the unit-name tag from a CCM ID_RESP payload.
// Each tag is encoded as len(2, big-endian) | tag(1) | value(len-1), as
// parsed by libosmocore's ipa_ccm_id_resp_parse; the value is a
// NUL-terminated string. Returns "" when the unit name is absent.
func parseIDResp(payload []byte) string {
	i := 1 // skip the msg type byte
	for len(payload)-i >= 3 {
		tagLen := int(binary.BigEndian.Uint16(payload[i : i+2]))
		tag := payload[i+2]
		i += 3
		if tagLen < 1 {
			break
		}
		valueLen := tagLen - 1
		if valueLen > len(payload)-i {
			break
		}
		val := payload[i : i+valueLen]
		i += valueLen
		if tag == ipaTagUnitName {
			return string(bytes.TrimRight(val, "\x00"))
		}
	}
	return ""
}
