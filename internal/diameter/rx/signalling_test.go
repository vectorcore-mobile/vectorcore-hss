package rx

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/diameter/basedict"
	"github.com/svinson1121/vectorcore-hss/internal/diameter/gx"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

var loadRxDictOnce sync.Once

// countingPeers records how often a Gx RAR was attempted; it never returns a
// connection, so the RAR itself fails after the lookup.
type countingPeers struct{ lookups int }

func (p *countingPeers) GetConn(string) (diam.Conn, bool) {
	p.lookups++
	return nil, false
}

// aarStore satisfies repository.Repository with the binding store's
// lookups; the AAR path calls nothing else.
type aarStore struct {
	repository.Repository
	b *bindingStore
}

func (s aarStore) GetServingAPNByUEIP(ctx context.Context, ip string) (*models.ServingAPN, error) {
	return s.b.GetServingAPNByUEIP(ctx, ip)
}
func (s aarStore) GetServingAPNByUEIPv6Prefix(ctx context.Context, p string) (*models.ServingAPN, error) {
	return s.b.GetServingAPNByUEIPv6Prefix(ctx, p)
}
func (s aarStore) GetServingAPNByIdentity(ctx context.Context, id string) (*models.ServingAPN, error) {
	return s.b.GetServingAPNByIdentity(ctx, id)
}
func (s aarStore) GetSubscriberByIMSI(ctx context.Context, imsi string) (*models.Subscriber, error) {
	return s.b.GetSubscriberByIMSI(ctx, imsi)
}
func (s aarStore) GetSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.Subscriber, error) {
	return s.b.GetSubscriberByMSISDN(ctx, msisdn)
}

type mediaSub struct {
	flowUsage *uint32
	fds       []string
}

func uptr(v uint32) *uint32 { return &v }

func mediaComponent(number uint32, mediaType *uint32, subs ...mediaSub) *diam.AVP {
	avps := []*diam.AVP{
		diam.NewAVP(avpMediaComponentNumber, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(number)),
	}
	for i, sc := range subs {
		scAVPs := []*diam.AVP{
			diam.NewAVP(avpFlowNumber, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(i+1)),
		}
		for _, fd := range sc.fds {
			scAVPs = append(scAVPs, diam.NewAVP(avpFlowDescription, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.IPFilterRule(fd)))
		}
		if sc.flowUsage != nil {
			scAVPs = append(scAVPs, diam.NewAVP(512, avp.Vbit, Vendor3GPP, datatype.Enumerated(*sc.flowUsage)))
		}
		avps = append(avps, diam.NewAVP(avpMediaSubComponent, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: scAVPs}))
	}
	if mediaType != nil {
		avps = append(avps, diam.NewAVP(avpMediaType, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(*mediaType)))
	}
	return diam.NewAVP(avpMediaComponentDescription, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: avps})
}

func buildAAR(t *testing.T, sessionID string, components ...*diam.AVP) *diam.Message {
	t.Helper()
	loadRxDictOnce.Do(func() {
		// Same order as diameter.NewServer; Framed-IP-Address comes from Gx.
		if err := basedict.Load(); err != nil {
			t.Fatalf("load base dict: %v", err)
		}
		if err := gx.LoadDict(); err != nil {
			t.Fatalf("load Gx dict: %v", err)
		}
		if err := LoadDict(); err != nil {
			t.Fatalf("load Rx dict: %v", err)
		}
	})
	m := diam.NewRequest(diam.AA, AppIDRx, nil)
	m.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(sessionID))
	m.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity("pcscf.ims.mnc099.mcc246.3gppnetwork.org"))
	m.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity("ims.mnc099.mcc246.3gppnetwork.org"))
	m.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity("epc.mnc099.mcc246.3gppnetwork.org"))
	m.NewAVP(avp.SubscriptionID, avp.Mbit, 0, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avp.SubscriptionIDType, avp.Mbit, 0, datatype.Enumerated(SubscriptionIDTypeIMSI)),
		diam.NewAVP(avp.SubscriptionIDData, avp.Mbit, 0, datatype.UTF8String("246990200000011")),
	}})
	m.NewAVP(avp.FramedIPAddress, avp.Mbit, 0, datatype.OctetString(net.ParseIP("10.46.0.26").To4()))
	for _, c := range components {
		m.AddAVP(c)
	}
	// Round-trip through the wire format so Unmarshal sees what a P-CSCF sends.
	b, err := m.Serialize()
	if err != nil {
		t.Fatalf("serialize AAR: %v", err)
	}
	out, err := diam.ReadMessage(bytes.NewReader(b), m.Dictionary())
	if err != nil {
		t.Fatalf("read AAR: %v", err)
	}
	return out
}

func runAAR(t *testing.T, m *diam.Message) (peers *countingPeers, resultCode uint32) {
	t.Helper()
	peers = &countingPeers{}
	h := &Handlers{
		store:    aarStore{b: newCaptureBindingStore()},
		log:      zap.NewNop(),
		peers:    peers,
		sessions: map[string]*rxSession{},
	}
	ans, err := h.AAR(nil, m)
	if err != nil {
		t.Fatalf("AAR: %v", err)
	}
	rc, err := ans.FindAVP(avp.ResultCode, 0)
	if err != nil {
		t.Fatalf("AAA has no Result-Code: %v", err)
	}
	return peers, uint32(rc.Data.(datatype.Unsigned32))
}

var sipFlows = []string{
	"permit out 17 from 10.46.0.1 5060 to 10.46.0.26 5060",
	"permit in 17 from 10.46.0.26 5060 to 10.46.0.1 5060",
}

// Field report: the registration AAR made the P-GW create a dedicated QCI 5
// bearer next to the IMS default bearer (QCI 5, ARP 5), because the rule
// carried its own ARP 1. Signalling must install no Gx rule at all.
func TestAARSignallingInstallsNoRule(t *testing.T) {
	control := uint32(MediaTypeControl)
	cases := map[string]*diam.AVP{
		"Media-Type CONTROL": mediaComponent(0, &control, mediaSub{fds: sipFlows}),
		"Flow-Usage AF_SIGNALLING, no Media-Type": mediaComponent(0, nil,
			mediaSub{flowUsage: uptr(FlowUsageAFSignalling), fds: sipFlows}),
	}
	for name, mc := range cases {
		t.Run(name, func(t *testing.T) {
			peers, rc := runAAR(t, buildAAR(t, "pcscf;1;reg", mc))
			if rc != diam.Success {
				t.Errorf("Result-Code %d, want %d", rc, diam.Success)
			}
			if peers.lookups != 0 {
				t.Errorf("registration AAR attempted %d Gx RAR(s), want none", peers.lookups)
			}
		})
	}
}

// A call's audio (RTP plus an RTCP sub-component) must still get a rule.
func TestAARAudioStillInstallsRule(t *testing.T) {
	audio := uint32(MediaTypeAudio)
	mc := mediaComponent(1, &audio,
		mediaSub{fds: []string{"permit out 17 from 10.46.0.1 40000 to 10.46.0.26 50000"}},
		mediaSub{flowUsage: uptr(1), fds: []string{"permit out 17 from 10.46.0.1 40001 to 10.46.0.26 50001"}}, // RTCP
	)
	peers, _ := runAAR(t, buildAAR(t, "pcscf;1;call", mc))
	if peers.lookups != 1 {
		t.Errorf("call AAR attempted %d Gx RAR(s), want 1", peers.lookups)
	}
}

func TestIsSignallingComponent(t *testing.T) {
	sig := datatype.Enumerated(FlowUsageAFSignalling)
	rtcp := datatype.Enumerated(1)
	for _, c := range []struct {
		name string
		mc   MediaComponentDescription
		want bool
	}{
		{"CONTROL", MediaComponentDescription{MediaType: MediaTypeControl}, true},
		{"all AF_SIGNALLING", MediaComponentDescription{MediaSubComponents: []MediaSubComponent{{FlowUsage: &sig}, {FlowUsage: &sig}}}, true},
		{"audio, no sub-components", MediaComponentDescription{MediaType: MediaTypeAudio}, false},
		{"audio with RTCP", MediaComponentDescription{MediaSubComponents: []MediaSubComponent{{}, {FlowUsage: &rtcp}}}, false},
		{"mixed signalling and media", MediaComponentDescription{MediaSubComponents: []MediaSubComponent{{FlowUsage: &sig}, {}}}, false},
	} {
		if got := isSignallingComponent(c.mc); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if qci, _, _, _, _ := mediaTypeQoSDefaults(MediaTypeControl); qci != 0 {
		t.Errorf("CONTROL still maps to QCI %d", qci)
	}
}
