package pcf

// N5 tests replay the request bodies the docker_open5gs Kamailio P-CSCF
// sends (testdata/kamailio, see its README) over HTTP/2 prior knowledge, as
// Kamailio's http_client_request_v2pk does, against a fake SMF and a fake
// P-CSCF notification listener.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

const (
	ueA          = "10.46.0.26" // caller in the fixtures
	ueB          = "10.46.0.27" // callee
	fixtureAFURI = "http://10.46.0.1:7777"
)

type n5Lab struct {
	t      *testing.T
	pcfURL string
	smfURL string
	afURL  string
	client *http.Client
	smf    chan callbackCapture
	af     chan callbackCapture
}

func newN5Lab(t *testing.T) *n5Lab {
	t.Helper()
	s, db := newTestPCFServer(t)
	seedPCFPolicyData(t, db)
	mustCreatePCF(t, db, &models.APN{APN: "ims", APNAMBRDown: 1530000, APNAMBRUp: 3850000, QCI: 5, ARPPriority: 1})

	l := &n5Lab{t: t, smf: make(chan callbackCapture, 16), af: make(chan callbackCapture, 16)}
	l.smfURL = startCallbackServer(t, l.smf)
	l.afURL = startCallbackServer(t, l.af)

	r := chi.NewRouter()
	s.mountRoutes(r)
	pcf := httptest.NewServer(h2c.NewHandler(r, &http2.Server{}))
	t.Cleanup(pcf.Close)
	l.pcfURL = pcf.URL

	// HTTP/2 over cleartext with prior knowledge, like Kamailio.
	l.client = &http.Client{Timeout: 5 * time.Second, Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	return l
}

func startCallbackServer(t *testing.T, ch chan<- callbackCapture) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- callbackCapture{Path: r.URL.Path, UserAgent: r.Header.Get("User-Agent"), Body: body}
		w.WriteHeader(http.StatusNoContent)
	}), &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func (l *n5Lab) do(method, path, contentType, body string) (int, http.Header, []byte) {
	l.t.Helper()
	req, err := http.NewRequest(method, l.pcfURL+path, strings.NewReader(body))
	if err != nil {
		l.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Kamailio routes through the SCP with these; direct to the PCF they
	// must simply be tolerated.
	req.Header.Set("3gpp-sbi-discovery-target-nf-type", "PCF")
	req.Header.Set("3gpp-sbi-discovery-service-names", "npcf-policyauthorization")
	resp, err := l.client.Do(req)
	if err != nil {
		l.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// fixture returns a Kamailio body with its notifUri pointed at the fake AF.
func (l *n5Lab) fixture(name string) string {
	l.t.Helper()
	raw, err := os.ReadFile("testdata/kamailio/" + name)
	if err != nil {
		l.t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), fixtureAFURI, l.afURL)
}

// createSMPolicy plays the SMF creating the IMS PDU session's SM policy.
func (l *n5Lab) createSMPolicy(ueIP, dnn, notifyPath string) string {
	l.t.Helper()
	body, _ := json.Marshal(smPolicyContextData{
		Supi:            "imsi-001010000000001",
		Dnn:             dnn,
		PduSessionID:    5,
		PduSessionType:  "IPV4",
		Ipv4Address:     ueIP,
		NotificationURI: l.smfURL + notifyPath,
	})
	code, hdr, raw := l.do(http.MethodPost, "/npcf-smpolicycontrol/v1/sm-policies", "application/json", string(body))
	if code != http.StatusCreated {
		l.t.Fatalf("SM policy create: %d %s", code, raw)
	}
	return hdr.Get("Location")
}

// createAppSession POSTs a fixture like Kamailio and returns the app session
// id the way Kamailio extracts it: the last path segment of Location.
func (l *n5Lab) createAppSession(name string) string {
	l.t.Helper()
	code, hdr, raw := l.do(http.MethodPost, appSessionsPath, "application/json", l.fixture(name))
	if code != http.StatusCreated {
		l.t.Fatalf("POST %s: got %d %s, want 201", name, code, raw)
	}
	loc := hdr.Get("Location")
	if !strings.HasPrefix(loc, l.pcfURL+appSessionsPath+"/") {
		l.t.Fatalf("Location %q is not an absolute app-session URI", loc)
	}
	return path.Base(loc)
}

func (l *n5Lab) expectSMF() smfNotify {
	l.t.Helper()
	cb := mustReceiveCallback(l.t, l.smf)
	var n smfNotify
	if err := json.Unmarshal(cb.Body, &n); err != nil {
		l.t.Fatalf("decode SMF notify: %v %s", err, cb.Body)
	}
	n.path, n.raw = cb.Path, cb.Body
	return n
}

func expectNone(t *testing.T, ch <-chan callbackCapture, what string) {
	t.Helper()
	select {
	case cb := <-ch:
		t.Fatalf("unexpected %s callback %s: %s", what, cb.Path, cb.Body)
	case <-time.After(150 * time.Millisecond):
	}
}

// smfNotify decodes a delta SmPolicyUpdateNotify; nil map values are
// removals.
type smfNotify struct {
	ResourceURI      string `json:"resourceUri"`
	SmPolicyDecision struct {
		PccRules map[string]*pccRule     `json:"pccRules"`
		QosDecs  map[string]*qosDecision `json:"qosDecs"`
	} `json:"smPolicyDecision"`
	path string
	raw  []byte
}

func wantFlows(t *testing.T, got []flow, want ...flow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("flows: got %d %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("flow[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Registration: CONTROL/AF_SIGNALLING must get 201 (else Kamailio fails the
// REGISTER with 412) and install nothing — SIP uses the default QoS flow.
func TestN5RegisterInstallsNoRule(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "ims", "/smf")

	id := l.createAppSession("register.json")
	expectNone(t, l.smf, "SMF")

	code, _, raw := l.do(http.MethodGet, appSessionsPath+"/"+id, "", "")
	if code != http.StatusOK || !bytes.Contains(raw, []byte(`"AF_SIGNALLING"`)) {
		t.Fatalf("GET app session: %d %s", code, raw)
	}
	// Re-REGISTER: Kamailio PATCHes the same body and expects 200.
	if code, _, raw := l.do(http.MethodPatch, appSessionsPath+"/"+id, "application/merge-patch+json", l.fixture("register.json")); code != http.StatusOK {
		t.Fatalf("PATCH re-register: %d %s", code, raw)
	}
	expectNone(t, l.smf, "SMF")
}

// Lenient: a registration for a UE with no SM policy here (SMF using another
// PCF, PCF restarted) is still accepted, since it installs nothing anyway.
func TestN5RegisterWithoutPDUSessionAccepted(t *testing.T) {
	l := newN5Lab(t)
	l.createAppSession("register.json")
	expectNone(t, l.smf, "SMF")
}

// Call media without a PDU session cannot get a QoS flow: reject, so the
// P-CSCF knows the call has no QoS.
func TestN5CallWithoutPDUSessionRejected(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "internet", "/smf") // same IP, other DNN: must not bind
	code, _, raw := l.do(http.MethodPost, appSessionsPath, "application/json", l.fixture("mo_invite.json"))
	if code != http.StatusInternalServerError || !bytes.Contains(raw, []byte("PDU_SESSION_NOT_AVAILABLE")) {
		t.Fatalf("got %d %s, want 500 PDU_SESSION_NOT_AVAILABLE", code, raw)
	}
	expectNone(t, l.smf, "SMF")
}

func TestN5MOCallLifecycle(t *testing.T) {
	l := newN5Lab(t)
	smPolicy := l.createSMPolicy(ueA, "ims", "/smf")

	id := l.createAppSession("mo_invite.json")
	rule := "n5-" + id + "-1"
	n := l.expectSMF()
	if n.path != "/smf/update" || n.ResourceURI != smPolicy {
		t.Fatalf("notify %s resourceUri %q, want /smf/update %q", n.path, n.ResourceURI, smPolicy)
	}
	pcc, q := n.SmPolicyDecision.PccRules[rule], n.SmPolicyDecision.QosDecs[rule]
	if len(n.SmPolicyDecision.PccRules) != 1 || pcc == nil || q == nil {
		t.Fatalf("want exactly rule %s, got %s", rule, n.raw)
	}
	if len(pcc.RefQosData) != 1 || pcc.RefQosData[0] != rule || pcc.Precedence == nil || *pcc.Precedence != 40 {
		t.Errorf("pcc rule: %+v", pcc)
	}
	wantFlows(t, pcc.FlowInfos,
		flow{"permit out 17 from 10.46.0.27 40000 to 10.46.0.26 50000", "DOWNLINK"},
		flow{"permit out 17 from 10.46.0.27 40000 to 10.46.0.26 50000", "UPLINK"},
		flow{"permit out 17 from 10.46.0.27 40001 to 10.46.0.26 50001", "DOWNLINK"},
		flow{"permit out 17 from 10.46.0.27 40001 to 10.46.0.26 50001", "UPLINK"},
	)
	if q.Var5qi == nil || *q.Var5qi != 1 || q.GbrDl != "38 Kbps" || q.GbrUl != "38 Kbps" || q.MaxbrDl != "38 Kbps" {
		t.Errorf("qos: %+v (want 5QI 1, GBR/MBR 38 Kbps from b=AS)", q)
	}
	if q.Arp == nil || q.Arp.PriorityLevel != 14 || q.Arp.PreemptCap != "NOT_PREEMPT" || q.Arp.PreemptVuln != "PREEMPTABLE" {
		t.Errorf("arp: %+v (want 14, NOT_PREEMPT, PREEMPTABLE as over Rx)", q.Arp)
	}

	// The SM policy's own view now includes the call rule.
	if code, _, raw := l.do(http.MethodGet, strings.TrimPrefix(smPolicy, l.pcfURL), "", ""); code != http.StatusOK || !bytes.Contains(raw, []byte(rule)) {
		t.Errorf("GET SM policy: %d, rule %s missing: %s", code, rule, raw)
	}

	// re-INVITE: the callee's port changed; the rule is sent again.
	if code, _, raw := l.do(http.MethodPatch, appSessionsPath+"/"+id, "application/merge-patch+json", l.fixture("mo_reinvite.json")); code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", code, raw)
	}
	n = l.expectSMF()
	if got := n.SmPolicyDecision.PccRules[rule]; got == nil || !strings.Contains(got.FlowInfos[0].FlowDescription, "40010") {
		t.Fatalf("re-INVITE notify: %s", n.raw)
	}
	// Same SDP again (session refresh): nothing changes, nothing is sent.
	if code, _, _ := l.do(http.MethodPatch, appSessionsPath+"/"+id, "application/merge-patch+json", l.fixture("mo_reinvite.json")); code != http.StatusOK {
		t.Fatalf("PATCH refresh: %d", code)
	}
	expectNone(t, l.smf, "SMF")

	// BYE: Kamailio POSTs an empty body to /delete.
	if code, _, raw := l.do(http.MethodPost, appSessionsPath+"/"+id+"/delete", "", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, raw)
	}
	n = l.expectSMF()
	if !bytes.Contains(n.raw, []byte(`"pccRules":{"`+rule+`":null}`)) || !bytes.Contains(n.raw, []byte(`"qosDecs":{"`+rule+`":null}`)) {
		t.Fatalf("delete notify must remove %s with null: %s", rule, n.raw)
	}
	if code, _, _ := l.do(http.MethodGet, appSessionsPath+"/"+id, "", ""); code != http.StatusNotFound {
		t.Errorf("GET after delete: %d, want 404", code)
	}
}

// "0 Kbps" (no b=AS in the SDP) means the default, as over Rx.
func TestN5NoBandwidthUsesDefault(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "ims", "/smf")
	id := l.createAppSession("mo_invite_nobw.json")
	q := l.expectSMF().SmPolicyDecision.QosDecs["n5-"+id+"-1"]
	if q == nil || q.GbrDl != "128 Kbps" || q.MaxbrUl != "128 Kbps" {
		t.Fatalf("qos %+v, want 128 Kbps default", q)
	}
}

func TestN5VideoCall(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "ims", "/smf")
	id := l.createAppSession("mo_invite_video.json")
	n := l.expectSMF()
	audio, video := n.SmPolicyDecision.QosDecs["n5-"+id+"-1"], n.SmPolicyDecision.QosDecs["n5-"+id+"-2"]
	if audio == nil || *audio.Var5qi != 1 {
		t.Fatalf("audio rule: %s", n.raw)
	}
	// 1000 Kbps requested, capped at the video default like Rx.
	if video == nil || *video.Var5qi != 2 || video.Arp.PriorityLevel != 11 || video.GbrDl != "512 Kbps" {
		t.Fatalf("video rule %+v, want 5QI 2, ARP 11, 512 Kbps", video)
	}
}

// On MT calls Kamailio's gpsi is the caller's MSISDN; the rule must go to
// the callee's PDU session, found by ueIpv4.
func TestN5MTCallBindsByUEIP(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "ims", "/smf-a")
	l.createSMPolicy(ueB, "ims", "/smf-b")
	id := l.createAppSession("mt_invite.json")
	n := l.expectSMF()
	if n.path != "/smf-b/update" {
		t.Fatalf("MT rule sent to %s, want the callee's SM policy /smf-b/update", n.path)
	}
	wantFlows(t, n.SmPolicyDecision.PccRules["n5-"+id+"-1"].FlowInfos[:2],
		flow{"permit out 17 from 10.46.0.26 50000 to 10.46.0.27 40000", "DOWNLINK"},
		flow{"permit out 17 from 10.46.0.26 50000 to 10.46.0.27 40000", "UPLINK"},
	)
}

// PDU session released: the AF is told and the app session is gone.
func TestN5PDUSessionReleaseTerminatesAppSession(t *testing.T) {
	l := newN5Lab(t)
	smPolicy := l.createSMPolicy(ueA, "ims", "/smf")
	id := l.createAppSession("mo_invite.json")
	l.expectSMF()

	if code, _, raw := l.do(http.MethodPost, strings.TrimPrefix(smPolicy, l.pcfURL)+"/delete", "", ""); code != http.StatusNoContent {
		t.Fatalf("SM policy delete: %d %s", code, raw)
	}
	cb := mustReceiveCallback(t, l.af)
	var info afTerminationInfo
	json.Unmarshal(cb.Body, &info)
	if cb.Path != "/terminate" || info.TermCause != "PDU_SESSION_TERMINATION" || !strings.HasSuffix(info.ResURI, "/app-sessions/"+id) {
		t.Fatalf("AF terminate: %s %s", cb.Path, cb.Body)
	}
	if code, _, _ := l.do(http.MethodGet, appSessionsPath+"/"+id, "", ""); code != http.StatusNotFound {
		t.Errorf("GET after PDU session release: %d, want 404", code)
	}
}

// An SMF-initiated SM policy update rebuilds the decision from the DB; the
// call's rule must survive it.
func TestN5SMPolicyUpdateKeepsCallRule(t *testing.T) {
	l := newN5Lab(t)
	smPolicy := l.createSMPolicy(ueA, "ims", "/smf")
	id := l.createAppSession("mo_invite.json")
	l.expectSMF()
	code, _, raw := l.do(http.MethodPost, strings.TrimPrefix(smPolicy, l.pcfURL)+"/update", "application/json", `{"suppFeat":"1"}`)
	if code != http.StatusOK || !bytes.Contains(raw, []byte("n5-"+id+"-1")) {
		t.Fatalf("SM policy update: %d, call rule missing: %s", code, raw)
	}
}

func TestN5RejectsBadRequests(t *testing.T) {
	l := newN5Lab(t)
	l.createSMPolicy(ueA, "ims", "/smf")
	call := l.fixture("mo_invite.json")
	for name, c := range map[string]struct {
		method, path, body string
		want               int
	}{
		"no ascReqData":     {http.MethodPost, appSessionsPath, `{}`, 400},
		"no notifUri":       {http.MethodPost, appSessionsPath, strings.Replace(call, `"notifUri"`, `"x"`, 1), 400},
		"no suppFeat":       {http.MethodPost, appSessionsPath, strings.Replace(call, `"suppFeat"`, `"x"`, 1), 400},
		"no UE address":     {http.MethodPost, appSessionsPath, strings.Replace(call, `"ueIpv4"`, `"x"`, 1), 400},
		"bad flow":          {http.MethodPost, appSessionsPath, strings.Replace(call, "permit in 17", "deny in 17", 1), 400},
		"bad bandwidth":     {http.MethodPost, appSessionsPath, strings.Replace(call, `"38 Kbps"`, `"fast"`, 1), 400},
		"PATCH unknown id":  {http.MethodPatch, appSessionsPath + "/999", call, 404},
		"delete unknown id": {http.MethodPost, appSessionsPath + "/999/delete", "", 404},
	} {
		if code, _, raw := l.do(c.method, c.path, "application/json", c.body); code != c.want {
			t.Errorf("%s: got %d %s, want %d", name, code, raw, c.want)
		}
	}
	expectNone(t, l.smf, "SMF")
}

func TestN5FlowToPCC(t *testing.T) {
	for in, want := range map[string]flow{
		"permit out 17 from 10.0.0.2 40000 to 10.0.0.1 50000": {"permit out 17 from 10.0.0.2 40000 to 10.0.0.1 50000", "DOWNLINK"},
		"permit in 17 from 10.0.0.1 50000 to 10.0.0.2 40000":  {"permit out 17 from 10.0.0.2 40000 to 10.0.0.1 50000", "UPLINK"},
		"permit in ip from 10.0.0.1 to any":                   {"permit out ip from any to 10.0.0.1", "UPLINK"},
	} {
		if got, err := n5FlowToPCC(in); err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "deny in ip from a to b", "permit in ip to b", "permit in ip from a"} {
		if _, err := n5FlowToPCC(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if !sameDNN("ims", "IMS.mnc001.mcc001.gprs") || sameDNN("ims", "internet") {
		t.Error("sameDNN")
	}
}
