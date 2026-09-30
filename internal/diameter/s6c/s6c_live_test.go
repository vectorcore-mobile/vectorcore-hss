package s6c

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"github.com/fiorix/go-diameter/v4/diam/sm"
)

const (
	testVendor3GPP = uint32(10415)
	testAppIDS6c   = uint32(16777312)
)

// TestLiveSRISMByMSISDN exercises a real S6c SRI-SM transaction against a
// running HSS. It is skipped unless HSS_S6C_LIVE_ADDR is set.
func TestLiveSRISMByMSISDN(t *testing.T) {
	addr := os.Getenv("HSS_S6C_LIVE_ADDR")
	if addr == "" {
		t.Skip("set HSS_S6C_LIVE_ADDR (e.g. 10.90.250.32:3868) to run against a live HSS")
	}

	if err := LoadMSISDNSupplement(); err != nil {
		t.Fatalf("LoadMSISDNSupplement: %v", err)
	}

	originHost := getenvDefault("HSS_S6C_LIVE_ORIGIN_HOST", "smsc.test.net")
	originRealm := getenvDefault("HSS_S6C_LIVE_ORIGIN_REALM", "epc.mnc435.mcc311.3gppnetwork.org")
	destRealm := getenvDefault("HSS_S6C_LIVE_DEST_REALM", "epc.mnc435.mcc311.3gppnetwork.org")

	msisdn := getenvDefault("HSS_S6C_LIVE_MSISDN", "3342012832")
	wantIMSI := getenvDefault("HSS_S6C_LIVE_IMSI", "311435000070570")
	wantMMEName := getenvDefault("HSS_S6C_LIVE_MME_NAME", "s6a-vpc-si-01.epc.mnc435.mcc311.3gppnetwork.org")
	wantMMERealm := getenvDefault("HSS_S6C_LIVE_MME_REALM", "epc.mnc435.mcc311.3gppnetwork.org")

	settings := &sm.Settings{
		OriginHost:       datatype.DiameterIdentity(originHost),
		OriginRealm:      datatype.DiameterIdentity(originRealm),
		VendorID:         datatype.Unsigned32(testVendor3GPP),
		ProductName:      datatype.UTF8String("s6c-live-test"),
		OriginStateID:    datatype.Unsigned32(uint32(time.Now().Unix())),
		FirmwareRevision: 1,
	}
	mux := sm.New(settings)

	client := &sm.Client{
		Dict:               dict.Default,
		Handler:            mux,
		MaxRetransmits:     1,
		RetransmitInterval: time.Second,
		EnableWatchdog:     false,
		SupportedVendorID: []*diam.AVP{
			diam.NewAVP(avp.SupportedVendorID, avp.Mbit, 0, datatype.Unsigned32(testVendor3GPP)),
		},
		VendorSpecificApplicationID: []*diam.AVP{
			diam.NewAVP(avp.VendorSpecificApplicationID, avp.Mbit, 0, &diam.GroupedAVP{
				AVP: []*diam.AVP{
					diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(testAppIDS6c)),
					diam.NewAVP(avp.VendorID, avp.Mbit, 0, datatype.Unsigned32(testVendor3GPP)),
				},
			}),
		},
	}

	conn, err := client.DialNetwork("tcp", addr)
	if err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	defer conn.Close()

	req := diam.NewRequest(cmdSRISM, testAppIDS6c, dict.Default)
	req.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(sessionIDForLive(originHost)))
	req.NewAVP(avp.AuthSessionState, avp.Mbit, 0, datatype.Enumerated(1))
	req.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(originHost))
	req.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(originRealm))
	req.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity(destRealm))
	req.NewAVP(avpMSISDN, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(encodeMSISDNBytes(msisdn)))
	req.NewAVP(avpSMRPMTI, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(0))

	answer := make(chan *diam.Message, 1)
	mux.HandleIdx(diam.CommandIndex{AppID: testAppIDS6c, Code: cmdSRISM, Request: false}, diam.HandlerFunc(func(c diam.Conn, msg *diam.Message) {
		answer <- msg
	}))

	if _, err := req.WriteTo(conn); err != nil {
		t.Fatalf("write SRI-SM: %v", err)
	}

	var ans *diam.Message
	select {
	case ans = <-answer:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SRI-SM answer")
	}

	if rc, ok := liveResultCode(ans); !ok {
		t.Fatal("missing Result-Code / Experimental-Result")
	} else if rc != 2001 {
		t.Fatalf("unexpected result-code %d", rc)
	}

	userNameAVP, err := ans.FindAVP(avp.UserName, 0)
	if err != nil {
		t.Fatalf("missing User-Name AVP: %v", err)
	}
	if got := string(userNameAVP.Data.(datatype.UTF8String)); got != wantIMSI {
		t.Fatalf("User-Name/IMSI: got %q, want %q", got, wantIMSI)
	}

	if mwdAVP := findAVPDirect(ans, avpMWDStatus, Vendor3GPP); mwdAVP != nil {
		t.Fatalf("unexpected MWD-Status AVP for attached UE: %+v", mwdAVP)
	}

	servingNodeAVP := findAVPDirect(ans, avpServingNode, Vendor3GPP)
	if servingNodeAVP == nil {
		t.Fatalf("missing Serving-Node AVP; answer AVPs: %s", summarizeAVPs(ans))
	}
	grouped, err := decodeGroupedAVP(servingNodeAVP)
	if err != nil {
		t.Fatalf("decode Serving-Node grouped AVP: %v", err)
	}

	var mmeName string
	var mmeRealm string
	for _, child := range grouped.AVP {
		switch child.Code {
		case avpMMEName:
			mmeName = decodeDiameterIdentity(child.Data)
		case avpMMERealm:
			mmeRealm = decodeDiameterIdentity(child.Data)
		}
	}

	if mmeName == "" {
		t.Fatalf("missing MME-Name inside Serving-Node; children: %s", summarizeChildAVPs(grouped.AVP))
	}
	if mmeName != wantMMEName {
		t.Fatalf("MME-Name: got %q, want %q", mmeName, wantMMEName)
	}
	if mmeRealm != wantMMERealm {
		t.Fatalf("MME-Realm: got %q, want %q", mmeRealm, wantMMERealm)
	}

	t.Logf("SRI-SM attached UE verified: msisdn=%s imsi=%s mme_name=%s mme_realm=%s", msisdn, wantIMSI, mmeName, mmeRealm)
}

func sessionIDForLive(originHost string) string {
	return originHost + ";" + time.Now().Format("20060102150405.000000000")
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func liveResultCode(msg *diam.Message) (uint32, bool) {
	if a, err := msg.FindAVP(avp.ResultCode, 0); err == nil {
		if rc, ok := a.Data.(datatype.Unsigned32); ok {
			return uint32(rc), true
		}
	}
	if a, err := msg.FindAVP(avp.ExperimentalResult, 0); err == nil {
		if g, ok := a.Data.(*diam.GroupedAVP); ok {
			for _, child := range g.AVP {
				if child.Code == avp.ExperimentalResultCode {
					if rc, ok := child.Data.(datatype.Unsigned32); ok {
						return uint32(rc), true
					}
				}
			}
		}
	}
	return 0, false
}

func summarizeAVPs(msg *diam.Message) string {
	parts := make([]string, 0, len(msg.AVP))
	for _, a := range msg.AVP {
		parts = append(parts, strings.Join([]string{
			"code=" + itoa32(a.Code),
			"vendor=" + itoa32(a.VendorID),
		}, "/"))
	}
	return strings.Join(parts, ", ")
}

func summarizeChildAVPs(avps []*diam.AVP) string {
	parts := make([]string, 0, len(avps))
	for _, a := range avps {
		parts = append(parts, "code="+itoa32(a.Code)+"/vendor="+itoa32(a.VendorID)+"/type="+fmt.Sprintf("%T", a.Data))
	}
	return strings.Join(parts, ", ")
}

func itoa32(v uint32) string {
	return strconv.FormatUint(uint64(v), 10)
}

func decodeGroupedAVP(a *diam.AVP) (*diam.GroupedAVP, error) {
	if grp, ok := a.Data.(*diam.GroupedAVP); ok {
		return grp, nil
	}
	var buf []byte
	switch raw := a.Data.(type) {
	case datatype.Grouped:
		buf = []byte(raw)
	case datatype.Unknown:
		buf = []byte(raw)
	default:
		return nil, fmt.Errorf("unexpected grouped AVP data type %T", a.Data)
	}
	var out diam.GroupedAVP
	for len(buf) > 0 {
		child, err := diam.DecodeAVP(buf, testAppIDS6c, dict.Default)
		if err != nil {
			return nil, err
		}
		out.AVP = append(out.AVP, child)
		buf = buf[child.Len():]
	}
	return &out, nil
}

func decodeDiameterIdentity(v datatype.Type) string {
	switch x := v.(type) {
	case datatype.DiameterIdentity:
		return string(x)
	case datatype.Unknown:
		return string([]byte(x))
	default:
		return ""
	}
}
