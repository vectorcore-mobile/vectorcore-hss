package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// Decoded SmPolicyUpdateNotify delta (only the fields the suite checks).
type notifyBody struct {
	ResourceURI      string `json:"resourceUri"`
	SmPolicyDecision struct {
		PccRules map[string]*notifyRule `json:"pccRules"`
		QosDecs  map[string]*notifyQos  `json:"qosDecs"`
	} `json:"smPolicyDecision"`
}

type notifyRule struct {
	RefQosData []string `json:"refQosData"`
	FlowInfos  []struct {
		FlowDescription string `json:"flowDescription"`
		FlowDirection   string `json:"flowDirection"`
	} `json:"flowInfos"`
}

type notifyQos struct {
	Var5qi  int    `json:"5qi"`
	GbrUl   string `json:"gbrUl"`
	GbrDl   string `json:"gbrDl"`
	MaxbrUl string `json:"maxbrUl"`
	Arp     *struct {
		PriorityLevel int `json:"priorityLevel"`
	} `json:"arp"`
}

type suite struct {
	c      *client
	l      *listener
	p      ascParams
	smPol  string
	passed int

	regID, callID, ruleKey string
}

const (
	notifyWait = 3 * time.Second
	quietWait  = time.Second
)

func runSuite(c *client, args []string) error {
	fs := flag.NewFlagSet("suite", flag.ExitOnError)
	var cf callFlags
	// TEST-NET addresses so the suite cannot bind to a real UE's PDU session.
	cf.register(fs, "192.0.2.26", "192.0.2.27")
	listen := fs.String("listen", ":9090", "address n5sim listens on for the PCF's SMF and P-CSCF callbacks")
	advertise := fs.String("advertise", "", "base URI the PCF reaches the listener at (default: this host's address towards the PCF)")
	fs.Parse(args) //nolint:errcheck

	l, base, err := listenAndAdvertise(c, *listen, *advertise)
	if err != nil {
		return err
	}
	defer l.close()
	cf.notifURI = base + "/af"
	s := &suite{c: c, l: l, p: cf.params()}
	fmt.Printf("n5sim suite: PCF %s, UE %s, DNN %s, SUPI %s, callbacks at %s\n", c.base, s.p.UE, s.p.DNN, cf.supi, base)

	defer func() {
		if s.smPol != "" {
			_ = c.deleteSMPolicy(s.smPol)
		}
	}()

	steps := []struct {
		name string
		fn   func() error
	}{
		{"SMF creates the IMS PDU session's SM policy", func() error { return s.createSMPolicy(cf.supi, base) }},
		{"REGISTER: 201, no QoS flow", s.register},
		{"MO call: 201, one 5QI 1 GBR rule pushed to the SMF", s.call},
		{"re-INVITE: 200, changed flows pushed", s.reinvite},
		{"identical re-INVITE: 200, nothing pushed", s.sameReinvite},
		{"BYE: 204, rule removed (null)", s.bye},
		{"video call: audio 5QI 1 + video 5QI 2", s.videoCall},
		{"PDU session release: app sessions terminated towards the P-CSCF", s.release},
		{"REGISTER without a PDU session: accepted", s.registerNoSession},
		{"call without a PDU session: rejected", s.callNoSession},
	}
	for i, st := range steps {
		if err := st.fn(); err != nil {
			fmt.Printf("FAIL %2d. %s\n         %v\n", i+1, st.name, err)
			return fmt.Errorf("%d/%d steps passed", s.passed, len(steps))
		}
		s.passed++
		fmt.Printf("PASS %2d. %s\n", i+1, st.name)
	}
	fmt.Printf("all %d steps passed\n", s.passed)
	return nil
}

func (s *suite) createSMPolicy(supi, base string) error {
	loc, err := s.c.createSMPolicy(smPolicyBody(supi, s.p.DNN, s.p.UE, base+"/smf"))
	if err != nil {
		return fmt.Errorf("%w (is %s provisioned with APN %q?)", err, supi, s.p.DNN)
	}
	s.smPol = loc
	return nil
}

func (s *suite) register() error {
	id, err := s.c.createAppSession(registerBody(s.p))
	if err != nil {
		return err
	}
	s.regID = id
	return s.expectQuiet()
}

func (s *suite) call() error {
	id, err := s.c.createAppSession(callBody(s.p))
	if err != nil {
		return err
	}
	s.callID = id
	n, err := s.expectNotify()
	if err != nil {
		return err
	}
	rules := n.SmPolicyDecision.PccRules
	if len(rules) != 1 {
		return fmt.Errorf("want 1 PCC rule, got %d", len(rules))
	}
	for k, r := range rules {
		s.ruleKey = k
		if r == nil {
			return fmt.Errorf("rule %s is null on create", k)
		}
		if len(r.FlowInfos) != 4 {
			return fmt.Errorf("rule %s: want 4 flows (RTP+RTCP both ways), got %d", k, len(r.FlowInfos))
		}
		for _, f := range r.FlowInfos {
			if !strings.HasPrefix(f.FlowDescription, "permit out 17 from ") {
				return fmt.Errorf("rule %s: flow %q not in permit-out form", k, f.FlowDescription)
			}
		}
	}
	return s.checkQos(n, s.ruleKey, 1, 14, fmt.Sprintf("%d Kbps", s.p.BandwidthKbps))
}

func (s *suite) reinvite() error {
	s.p.Peer.Port += 10
	if err := s.c.patchAppSession(s.callID, callBody(s.p)); err != nil {
		return err
	}
	n, err := s.expectNotify()
	if err != nil {
		return err
	}
	r := n.SmPolicyDecision.PccRules[s.ruleKey]
	if r == nil {
		return fmt.Errorf("rule %s missing from the update", s.ruleKey)
	}
	want := fmt.Sprintf(" %d ", s.p.Peer.Port)
	for _, f := range r.FlowInfos {
		if strings.Contains(f.FlowDescription+" ", want) {
			return nil
		}
	}
	return fmt.Errorf("no flow carries the new remote port %d", s.p.Peer.Port)
}

func (s *suite) sameReinvite() error {
	if err := s.c.patchAppSession(s.callID, callBody(s.p)); err != nil {
		return err
	}
	return s.expectQuiet()
}

func (s *suite) bye() error {
	if err := s.c.deleteAppSession(s.callID); err != nil {
		return err
	}
	n, err := s.expectNotify()
	if err != nil {
		return err
	}
	r, ok := n.SmPolicyDecision.PccRules[s.ruleKey]
	if !ok || r != nil {
		return fmt.Errorf("want pccRules.%s = null, got %v", s.ruleKey, n.SmPolicyDecision.PccRules)
	}
	if r, err := s.c.n5(http.MethodGet, appSessionsPath+"/"+s.callID, "", nil); err != nil || r.Code != http.StatusNotFound {
		return fmt.Errorf("GET deleted app session: want 404, got %v %v", code(r), err)
	}
	return nil
}

func (s *suite) videoCall() error {
	p := s.p
	p.Video = true
	id, err := s.c.createAppSession(callBody(p))
	if err != nil {
		return err
	}
	n, err := s.expectNotify()
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	for k := range n.SmPolicyDecision.PccRules {
		if q := n.SmPolicyDecision.QosDecs[k]; q != nil {
			seen[q.Var5qi] = true
		}
	}
	if len(n.SmPolicyDecision.PccRules) != 2 || !seen[1] || !seen[2] {
		return fmt.Errorf("want 2 rules with 5QI 1 and 2, got %s", mustJSON(n.SmPolicyDecision))
	}
	if err := s.c.deleteAppSession(id); err != nil {
		return err
	}
	_, err = s.expectNotify()
	return err
}

func (s *suite) release() error {
	id, err := s.c.createAppSession(callBody(s.p))
	if err != nil {
		return err
	}
	s.callID = id
	if _, err := s.expectNotify(); err != nil {
		return err
	}
	if err := s.c.deleteSMPolicy(s.smPol); err != nil {
		return err
	}
	s.smPol = ""
	want := map[string]bool{s.regID: true, s.callID: true}
	for len(want) > 0 {
		cb, ok := receive(s.l.af, notifyWait)
		if !ok {
			return fmt.Errorf("no terminate for app sessions %v", keys(want))
		}
		if cb.Path != "/af/terminate" {
			return fmt.Errorf("P-CSCF callback on %s, want /af/terminate", cb.Path)
		}
		var t struct {
			TermCause string `json:"termCause"`
			ResURI    string `json:"resUri"`
		}
		if err := json.Unmarshal(cb.Body, &t); err != nil {
			return fmt.Errorf("decode terminate: %v: %s", err, cb.Body)
		}
		if t.TermCause != "PDU_SESSION_TERMINATION" {
			return fmt.Errorf("termCause %q, want PDU_SESSION_TERMINATION", t.TermCause)
		}
		delete(want, path.Base(t.ResURI))
	}
	// The PCF also tells the SMF its SM policy is gone; drop that.
	for {
		if _, ok := receive(s.l.smf, quietWait); !ok {
			break
		}
	}
	return nil
}

func (s *suite) registerNoSession() error {
	id, err := s.c.createAppSession(registerBody(s.p))
	if err != nil {
		return err
	}
	return s.c.deleteAppSession(id)
}

func (s *suite) callNoSession() error {
	r, err := s.c.n5(http.MethodPost, appSessionsPath, "application/json", callBody(s.p))
	if err != nil {
		return err
	}
	if r.Code == http.StatusCreated {
		_ = s.c.deleteAppSession(path.Base(r.Location))
		return fmt.Errorf("got 201; media with no PDU session must be refused")
	}
	return nil
}

func (s *suite) expectNotify() (*notifyBody, error) {
	cb, ok := receive(s.l.smf, notifyWait)
	if !ok {
		return nil, fmt.Errorf("no SmPolicyUpdateNotify within %s", notifyWait)
	}
	if cb.Path != "/smf/update" {
		return nil, fmt.Errorf("SMF callback on %s, want /smf/update", cb.Path)
	}
	var n notifyBody
	if err := json.Unmarshal(cb.Body, &n); err != nil {
		return nil, fmt.Errorf("decode notify: %v: %s", err, cb.Body)
	}
	if n.ResourceURI == "" {
		return nil, fmt.Errorf("notify without resourceUri")
	}
	return &n, nil
}

func (s *suite) expectQuiet() error {
	if cb, ok := receive(s.l.smf, quietWait); ok {
		return fmt.Errorf("unexpected SMF notify %s: %s", cb.Path, cb.Body)
	}
	return nil
}

func (s *suite) checkQos(n *notifyBody, rule string, fiveQI, arp int, gbr string) error {
	q := n.SmPolicyDecision.QosDecs[rule]
	if q == nil {
		return fmt.Errorf("no qosDecs entry for rule %s", rule)
	}
	if q.Var5qi != fiveQI {
		return fmt.Errorf("5QI %d, want %d", q.Var5qi, fiveQI)
	}
	if q.Arp == nil || q.Arp.PriorityLevel != arp {
		return fmt.Errorf("ARP %+v, want priority %d", q.Arp, arp)
	}
	if s.p.BandwidthKbps > 0 && (q.GbrUl != gbr || q.GbrDl != gbr) {
		return fmt.Errorf("GBR %s/%s, want %s", q.GbrUl, q.GbrDl, gbr)
	}
	return nil
}

func code(r *response) int {
	if r == nil {
		return 0
	}
	return r.Code
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
