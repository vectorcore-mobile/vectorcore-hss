package gsup

import (
	"bytes"
	"testing"
)

func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		{MsgSendAuthInfoReq},
		NewMsg(MsgSendAuthInfoReq).
			Add(IEIMSITag, encodeIMSI("311435000000001")).
			AddByte(IENumberOfRequestedVec, 5).
			Bytes(),
		NewMsg(MsgUpdateLocReq).
			Add(IEIMSITag, encodeIMSI("001010000000001")).
			Bytes(),
		{MsgSendAuthInfoReq, IEIMSITag, 0},
		{0x30, 0x30},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		msg, err := Decode(data)
		if err != nil {
			return
		}

		rebuilt := NewMsg(msg.Type)
		for _, ie := range msg.IEs {
			rebuilt.Add(ie.Tag, ie.Data)
		}
		if got := rebuilt.Bytes(); !bytes.Equal(got, data) {
			t.Fatalf("successful decode was not lossless: input %x, rebuilt %x", data, got)
		}
	})
}

func FuzzParseIDResp(f *testing.F) {
	f.Add([]byte{ccmMsgIDResp})
	f.Add([]byte{ccmMsgIDResp, 0x00, 0x05, ipaTagUnitName, 'h', 's', 's', 0})
	f.Add([]byte{ccmMsgIDResp, 0x00, 0x01, ipaTagUnitName})
	f.Add([]byte{ccmMsgIDResp, 0x00, 0x02, ipaTagSerial, '1'})
	f.Add([]byte{ccmMsgIDResp, 0xFF, 0xFF, ipaTagUnitName, 'x'})
	f.Add([]byte{'0', 0, '0'})

	f.Fuzz(func(t *testing.T, payload []byte) {
		_ = parseIDResp(payload)
	})
}

func FuzzIMSIRoundTrip(f *testing.F) {
	f.Add("311435000000001")
	f.Add("001010000000001")
	f.Add("99999999999999")
	f.Add("1")

	f.Fuzz(func(t *testing.T, imsi string) {
		if len(imsi) == 0 || len(imsi) > 15 {
			return
		}
		for _, c := range imsi {
			if c < '0' || c > '9' {
				return
			}
		}

		if got := decodeIMSI(encodeIMSI(imsi)); got != imsi {
			t.Fatalf("IMSI round trip: got %q, want %q", got, imsi)
		}
	})
}
