package pcf

// policyauth.go -- Npcf_PolicyAuthorization (N5, TS 29.514) for IMS.
//
// The P-CSCF (AF) creates an app session per registration and per call. It
// is bound to the UE's SM policy association by UE IP (+ DNN), and each
// call media component becomes a PCC rule pushed to the SMF as a delta
// SmPolicyUpdateNotify: only the app session's rules, removed by sending
// them as null. Signalling components get no rule -- SIP rides the IMS
// PDU session's default QoS flow, configured on the APN/DNN.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/policy"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

const appSessionsPath = "/npcf-policyauthorization/v1/app-sessions"

type appSessionContext struct {
	AscReqData  *appSessionContextReqData  `json:"ascReqData,omitempty"`
	AscRespData *appSessionContextRespData `json:"ascRespData,omitempty"`
}

type appSessionContextReqData struct {
	AfAppID       string                     `json:"afAppId,omitempty"`
	Dnn           string                     `json:"dnn,omitempty"`
	MedComponents map[string]*mediaComponent `json:"medComponents,omitempty"`
	EvSubsc       json.RawMessage            `json:"evSubsc,omitempty"`
	NotifURI      string                     `json:"notifUri"`
	SponStatus    string                     `json:"sponStatus,omitempty"`
	Supi          string                     `json:"supi,omitempty"`
	Gpsi          string                     `json:"gpsi,omitempty"`
	SuppFeat      string                     `json:"suppFeat"`
	UeIpv4        string                     `json:"ueIpv4,omitempty"`
	UeIpv6        string                     `json:"ueIpv6,omitempty"`
}

type appSessionContextRespData struct {
	SuppFeat string `json:"suppFeat,omitempty"`
}

type mediaComponent struct {
	MedCompN    int                           `json:"medCompN"`
	MedType     string                        `json:"medType,omitempty"`
	Codecs      []string                      `json:"codecs,omitempty"`
	FStatus     string                        `json:"fStatus,omitempty"`
	MarBwDl     string                        `json:"marBwDl,omitempty"`
	MarBwUl     string                        `json:"marBwUl,omitempty"`
	MedSubComps map[string]*mediaSubComponent `json:"medSubComps,omitempty"`
}

type mediaSubComponent struct {
	FNum      int      `json:"fNum"`
	FDescs    []string `json:"fDescs,omitempty"`
	FStatus   string   `json:"fStatus,omitempty"`
	MarBwDl   string   `json:"marBwDl,omitempty"`
	MarBwUl   string   `json:"marBwUl,omitempty"`
	FlowUsage string   `json:"flowUsage,omitempty"`
}

// appSessionContextUpdateDataPatch is the PATCH body (merge-patch). A
// medComponents entry set to null removes that component.
type appSessionContextUpdateDataPatch struct {
	AscReqData *struct {
		MedComponents map[string]json.RawMessage `json:"medComponents,omitempty"`
		EvSubsc       json.RawMessage            `json:"evSubsc,omitempty"`
		NotifURI      *string                    `json:"notifUri,omitempty"`
		AfAppID       *string                    `json:"afAppId,omitempty"`
		SponStatus    *string                    `json:"sponStatus,omitempty"`
	} `json:"ascReqData,omitempty"`
}

type appSession struct {
	ID          string
	ResourceURI string
	SMPolicyID  string // "" when no SM policy matched (signalling only)
	Req         appSessionContextReqData
	Rules       map[string]appRule // pccRuleId → rule
}

type appRule struct {
	PCC pccRule
	QoS qosDecision
}

// smPolicyDecisionDelta carries only the changed rules; a nil entry
// marshals as null, which tells the SMF to remove that rule.
type smPolicyDecisionDelta struct {
	PccRules map[string]*pccRule     `json:"pccRules,omitempty"`
	QosDecs  map[string]*qosDecision `json:"qosDecs,omitempty"`
}

type smPolicyDeltaNotification struct {
	ResourceURI      string                 `json:"resourceUri"`
	SmPolicyDecision *smPolicyDecisionDelta `json:"smPolicyDecision"`
}

type afTerminationInfo struct {
	TermCause string `json:"termCause"`
	ResURI    string `json:"resUri"`
}

func (s *Server) registerPolicyAuthRoutes(r chi.Router) {
	const scope = "npcf-policyauthorization"
	r.Post(appSessionsPath, s.wrapOAuth(scope, s.handleAppSessionCreate))
	r.Get(appSessionsPath+"/{appSessionId}", s.wrapOAuth(scope, s.handleAppSessionGet))
	r.Patch(appSessionsPath+"/{appSessionId}", s.wrapOAuth(scope, s.handleAppSessionPatch))
	r.Post(appSessionsPath+"/{appSessionId}/delete", s.wrapOAuth(scope, s.handleAppSessionDelete))
}

func (s *Server) handleAppSessionCreate(w http.ResponseWriter, r *http.Request) {
	var in appSessionContext
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.AscReqData == nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_MSG_FORMAT", "no ascReqData")
		return
	}
	req := *in.AscReqData
	switch {
	case strings.TrimSpace(req.NotifURI) == "":
		writeProblem(w, http.StatusBadRequest, "MANDATORY_IE_MISSING", "notifUri")
		return
	case strings.TrimSpace(req.SuppFeat) == "":
		writeProblem(w, http.StatusBadRequest, "MANDATORY_IE_MISSING", "suppFeat")
		return
	case len(req.MedComponents) == 0:
		writeProblem(w, http.StatusBadRequest, "MANDATORY_IE_MISSING", "medComponents")
		return
	case req.UeIpv4 == "" && req.UeIpv6 == "":
		writeProblem(w, http.StatusBadRequest, "MANDATORY_IE_MISSING", "ueIpv4 or ueIpv6")
		return
	}

	id := strconv.FormatUint(s.appSeq.Add(1), 10)
	rules, err := buildAppRules(id, req.MedComponents)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_MSG_FORMAT", err.Error())
		return
	}

	assoc := s.findSMPolicyForUE(req.UeIpv4, req.UeIpv6, req.Dnn)
	logFields := []zap.Field{zap.String("app_session", id), zap.String("ue_ipv4", req.UeIpv4),
		zap.String("ue_ipv6", req.UeIpv6), zap.String("dnn", req.Dnn), zap.Int("rules", len(rules))}
	if assoc == nil {
		if len(rules) > 0 {
			s.log.Warn("pcf: N5 app session rejected, no SM policy for UE", logFields...)
			writeProblem(w, http.StatusInternalServerError, "PDU_SESSION_NOT_AVAILABLE", "no SM policy association for the UE address")
			return
		}
		// Signalling only: nothing to install, so a missing PDU session
		// must not fail the P-CSCF's REGISTER (it answers 412 otherwise).
		s.log.Warn("pcf: N5 app session without SM policy accepted (signalling only, no rules)", logFields...)
	}

	sess := &appSession{
		ID:          id,
		ResourceURI: requestBaseURL(r) + appSessionsPath + "/" + id,
		Req:         req,
		Rules:       rules,
	}
	if assoc != nil {
		sess.SMPolicyID = assoc.ID
	}
	s.appMu.Lock()
	s.appSessions[id] = sess
	s.appMu.Unlock()

	if assoc != nil && len(rules) > 0 {
		s.notifyAppRules(r.Context(), assoc, nil, rules)
	}
	s.log.Info("pcf: N5 app session created", append(logFields, zap.String("sm_policy", sess.SMPolicyID))...)

	w.Header().Set("Location", sess.ResourceURI)
	writeAppSession(w, http.StatusCreated, sess)
}

func (s *Server) handleAppSessionGet(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getAppSession(chi.URLParam(r, "appSessionId"))
	if !ok {
		writeProblem(w, http.StatusNotFound, "APPLICATION_SESSION_CONTEXT_NOT_FOUND", "")
		return
	}
	writeAppSession(w, http.StatusOK, sess)
}

func (s *Server) handleAppSessionPatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appSessionId")
	cur, ok := s.getAppSession(id)
	if !ok {
		writeProblem(w, http.StatusNotFound, "APPLICATION_SESSION_CONTEXT_NOT_FOUND", "")
		return
	}
	var patch appSessionContextUpdateDataPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil && err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "INVALID_MSG_FORMAT", err.Error())
		return
	}

	next := *cur
	next.Req.MedComponents = make(map[string]*mediaComponent, len(cur.Req.MedComponents))
	for k, v := range cur.Req.MedComponents {
		next.Req.MedComponents[k] = v
	}
	if u := patch.AscReqData; u != nil {
		for k, raw := range u.MedComponents {
			if string(raw) == "null" {
				delete(next.Req.MedComponents, k)
				continue
			}
			var mc mediaComponent
			if err := json.Unmarshal(raw, &mc); err != nil {
				writeProblem(w, http.StatusBadRequest, "INVALID_MSG_FORMAT", "medComponents."+k+": "+err.Error())
				return
			}
			next.Req.MedComponents[k] = &mc
		}
		if len(u.EvSubsc) > 0 {
			next.Req.EvSubsc = u.EvSubsc
		}
		if u.NotifURI != nil {
			next.Req.NotifURI = *u.NotifURI
		}
		if u.AfAppID != nil {
			next.Req.AfAppID = *u.AfAppID
		}
		if u.SponStatus != nil {
			next.Req.SponStatus = *u.SponStatus
		}
	}
	rules, err := buildAppRules(id, next.Req.MedComponents)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_MSG_FORMAT", err.Error())
		return
	}
	next.Rules = rules

	assoc, bound := s.getAssociation(next.SMPolicyID)
	if !bound {
		// Unbound so far (registration without a PDU session) or the SM
		// policy went away: bind again now.
		assoc = s.findSMPolicyForUE(next.Req.UeIpv4, next.Req.UeIpv6, next.Req.Dnn)
		next.SMPolicyID = ""
		if assoc != nil {
			next.SMPolicyID = assoc.ID
		} else if len(rules) > 0 {
			s.log.Warn("pcf: N5 app session update rejected, no SM policy for UE",
				zap.String("app_session", id), zap.String("ue_ipv4", next.Req.UeIpv4))
			writeProblem(w, http.StatusInternalServerError, "PDU_SESSION_NOT_AVAILABLE", "no SM policy association for the UE address")
			return
		}
	}

	s.appMu.Lock()
	s.appSessions[id] = &next
	s.appMu.Unlock()

	if assoc != nil {
		old := cur.Rules
		if !bound || cur.SMPolicyID != next.SMPolicyID {
			old = nil // the SMF never got the old rules on this session
		}
		s.notifyAppRules(r.Context(), assoc, old, rules)
	}
	s.log.Info("pcf: N5 app session updated", zap.String("app_session", id),
		zap.String("sm_policy", next.SMPolicyID), zap.Int("rules", len(rules)))
	writeAppSession(w, http.StatusOK, &next)
}

func (s *Server) handleAppSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "appSessionId")
	s.appMu.Lock()
	sess, ok := s.appSessions[id]
	delete(s.appSessions, id)
	s.appMu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "APPLICATION_SESSION_CONTEXT_NOT_FOUND", "")
		return
	}
	if assoc, bound := s.getAssociation(sess.SMPolicyID); bound && len(sess.Rules) > 0 {
		s.notifyAppRules(r.Context(), assoc, sess.Rules, nil)
	}
	s.log.Info("pcf: N5 app session deleted", zap.String("app_session", id),
		zap.String("sm_policy", sess.SMPolicyID), zap.Int("rules_removed", len(sess.Rules)))
	w.WriteHeader(http.StatusNoContent)
}

// notifyAppRules sends the SMF the difference between an app session's old
// and new rules: changed or added rules in full, dropped ones as null.
func (s *Server) notifyAppRules(ctx context.Context, assoc *smPolicyAssociation, old, cur map[string]appRule) {
	delta := smPolicyDecisionDelta{
		PccRules: map[string]*pccRule{},
		QosDecs:  map[string]*qosDecision{},
	}
	for id, r := range cur {
		if prev, ok := old[id]; ok && reflect.DeepEqual(prev, r) {
			continue
		}
		pcc, q := r.PCC, r.QoS
		delta.PccRules[id] = &pcc
		delta.QosDecs[r.QoS.QosID] = &q
	}
	for id, r := range old {
		if _, ok := cur[id]; !ok {
			delta.PccRules[id] = nil
			delta.QosDecs[r.QoS.QosID] = nil
		}
	}
	if len(delta.PccRules) == 0 {
		return
	}
	s.notifySMPolicy(ctx, notificationTarget(assoc.Context.NotificationURI, "update"), smPolicyDeltaNotification{
		ResourceURI:      assoc.ResourceURI,
		SmPolicyDecision: &delta,
	})
}

// terminateAppSessionsFor drops the app sessions bound to an SM policy
// association that was deleted, telling each AF (TS 29.514 §4.2.5.3).
func (s *Server) terminateAppSessionsFor(smPolicyID string) {
	var gone []*appSession
	s.appMu.Lock()
	for id, sess := range s.appSessions {
		if sess.SMPolicyID == smPolicyID {
			gone = append(gone, sess)
			delete(s.appSessions, id)
		}
	}
	s.appMu.Unlock()
	for _, sess := range gone {
		go func(sess *appSession) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.sendCallback(ctx, notificationTarget(sess.Req.NotifURI, "terminate"), afTerminationInfo{
				TermCause: "PDU_SESSION_TERMINATION",
				ResURI:    sess.ResourceURI,
			}, "AF", "npcf-policyauthorization")
		}(sess)
		s.log.Info("pcf: N5 app session terminated with its PDU session",
			zap.String("app_session", sess.ID), zap.String("sm_policy", smPolicyID))
	}
}

// decisionWithAppRules returns the association's decision plus the rules
// of the N5 app sessions bound to it.
func (s *Server) decisionWithAppRules(assoc *smPolicyAssociation) smPolicyDecision {
	out := assoc.Decision
	out.PccRules = make(map[string]pccRule, len(assoc.Decision.PccRules))
	out.QosDecs = make(map[string]qosDecision, len(assoc.Decision.QosDecs))
	for k, v := range assoc.Decision.PccRules {
		out.PccRules[k] = v
	}
	for k, v := range assoc.Decision.QosDecs {
		out.QosDecs[k] = v
	}
	s.appMu.Lock()
	defer s.appMu.Unlock()
	for _, sess := range s.appSessions {
		if sess.SMPolicyID != assoc.ID {
			continue
		}
		for id, r := range sess.Rules {
			out.PccRules[id] = r.PCC
			out.QosDecs[r.QoS.QosID] = r.QoS
		}
	}
	return out
}

// findSMPolicyForUE finds the SM policy association of the PDU session
// holding the UE address. dnn narrows the match when both sides carry one.
// The newest association wins if a stale one has the same address.
func (s *Server) findSMPolicyForUE(ueIPv4, ueIPv6, dnn string) *smPolicyAssociation {
	var v6 netip.Addr
	if ueIPv6 != "" {
		v6, _ = netip.ParseAddr(ueIPv6)
	}
	s.assocMu.RLock()
	defer s.assocMu.RUnlock()
	var best *smPolicyAssociation
	var bestSeq uint64
	for _, a := range s.associations {
		if dnn != "" && a.Context.Dnn != "" && !sameDNN(dnn, a.Context.Dnn) {
			continue
		}
		match := ueIPv4 != "" && a.Context.Ipv4Address == ueIPv4
		if !match && v6.IsValid() && a.Context.Ipv6AddressPrefix != "" {
			if p, err := netip.ParsePrefix(a.Context.Ipv6AddressPrefix); err == nil && p.Contains(v6) {
				match = true
			}
		}
		if !match {
			continue
		}
		if seq, _ := strconv.ParseUint(a.ID, 10, 64); best == nil || seq > bestSeq {
			best, bestSeq = a, seq
		}
	}
	return best
}

// sameDNN compares DNNs by network identifier, so "ims" matches
// "ims.mnc001.mcc001.gprs".
func sameDNN(a, b string) bool {
	label := func(s string) string {
		if i := strings.IndexByte(s, '.'); i > 0 {
			s = s[:i]
		}
		return strings.ToLower(s)
	}
	return label(a) == label(b)
}

// buildAppRules maps an app session's media components to PCC rules, one
// per non-signalling component, with the QoS VoLTE uses over Rx.
func buildAppRules(appID string, comps map[string]*mediaComponent) (map[string]appRule, error) {
	rules := make(map[string]appRule)
	for key, mc := range comps {
		if mc == nil || mc.FStatus == "REMOVED" {
			continue
		}
		mt := n5IMSMediaType(mc.MedType)
		subKeys := make([]string, 0, len(mc.MedSubComps))
		usages := make([]policy.IMSFlowUsage, 0, len(mc.MedSubComps))
		for k, sc := range mc.MedSubComps {
			if sc == nil {
				continue
			}
			subKeys = append(subKeys, k)
			usages = append(usages, n5FlowUsage(sc.FlowUsage))
		}
		if policy.IsIMSSignalling(mt, usages) {
			continue
		}
		q, ok := policy.IMSMediaQoSFor(mt)
		if !ok {
			continue
		}
		sort.Strings(subKeys)

		var flows []flow
		dl, ul := mc.MarBwDl, mc.MarBwUl
		var subDL, subUL uint64
		for _, k := range subKeys {
			sc := mc.MedSubComps[k]
			if sc.FStatus == "REMOVED" {
				continue
			}
			for _, fd := range sc.FDescs {
				f, err := n5FlowToPCC(fd)
				if err != nil {
					return nil, fmt.Errorf("medComponents.%s.medSubComps.%s: %w", key, k, err)
				}
				flows = append(flows, f)
			}
			for _, b := range []struct {
				s   string
				max *uint64
			}{{sc.MarBwDl, &subDL}, {sc.MarBwUl, &subUL}} {
				if b.s == "" {
					continue
				}
				v, err := sbi.ParseBitRate(b.s)
				if err != nil {
					return nil, fmt.Errorf("medComponents.%s.medSubComps.%s: %w", key, k, err)
				}
				if v > *b.max {
					*b.max = v
				}
			}
		}
		if len(flows) == 0 {
			continue
		}
		bwDL, err := mediaBandwidth(dl, subDL, q.DefaultBW)
		if err != nil {
			return nil, fmt.Errorf("medComponents.%s: %w", key, err)
		}
		bwUL, err := mediaBandwidth(ul, subUL, q.DefaultBW)
		if err != nil {
			return nil, fmt.Errorf("medComponents.%s: %w", key, err)
		}

		id := fmt.Sprintf("n5-%s-%d", appID, mc.MedCompN)
		if _, dup := rules[id]; dup {
			return nil, fmt.Errorf("duplicate medCompN %d", mc.MedCompN)
		}
		qd := qosDecision{
			QosID:   id,
			Var5qi:  int32ptr(int32(q.QCI)),
			MaxbrUl: sbi.FormatBitRate(int(bwUL)),
			MaxbrDl: sbi.FormatBitRate(int(bwDL)),
			// As over Rx: dedicated IMS flows never pre-empt and may be pre-empted.
			Arp: &arp{PriorityLevel: int32(q.ARPPriority), PreemptCap: "NOT_PREEMPT", PreemptVuln: "PREEMPTABLE"},
		}
		if q.GBR {
			qd.GbrUl, qd.GbrDl = qd.MaxbrUl, qd.MaxbrDl
		}
		rules[id] = appRule{
			PCC: pccRule{
				PccRuleID:  id,
				Precedence: int32ptr(int32(q.Precedence)),
				RefQosData: []string{id},
				FlowInfos:  flows,
			},
			QoS: qd,
		}
	}
	return rules, nil
}

// mediaBandwidth picks the component's bandwidth (else the largest
// sub-component one), falling back to and capped at the default, the same
// as the Rx AAR path. "0 Kbps" (no b=AS in the SDP) means the default.
func mediaBandwidth(component string, sub uint64, def uint32) (uint64, error) {
	bw := sub
	if component != "" {
		v, err := sbi.ParseBitRate(component)
		if err != nil {
			return 0, err
		}
		bw = v
	}
	if bw == 0 || bw > uint64(def) {
		bw = uint64(def)
	}
	return bw, nil
}

// n5FlowToPCC converts an AF flow description to a PCC flow (TS 29.512):
// "permit out" is the downlink filter as is; "permit in … from A to B" is
// the uplink filter rewritten as "permit out … from B to A", as the
// Open5GS PCF does.
func n5FlowToPCC(fd string) (flow, error) {
	fd = strings.TrimSpace(fd)
	if strings.HasPrefix(fd, "permit out ") {
		return flow{FlowDescription: fd, FlowDirection: "DOWNLINK"}, nil
	}
	rest, ok := strings.CutPrefix(fd, "permit in ")
	if !ok {
		return flow{}, fmt.Errorf("flow description %q: want \"permit in|out ...\"", fd)
	}
	proto, ends, ok := strings.Cut(rest, " from ")
	if !ok {
		return flow{}, fmt.Errorf("flow description %q: missing \"from\"", fd)
	}
	src, dst, ok := strings.Cut(ends, " to ")
	if !ok {
		return flow{}, fmt.Errorf("flow description %q: missing \"to\"", fd)
	}
	return flow{
		FlowDescription: "permit out " + proto + " from " + dst + " to " + src,
		FlowDirection:   "UPLINK",
	}, nil
}

func n5IMSMediaType(medType string) policy.IMSMediaType {
	switch strings.ToUpper(medType) {
	case "AUDIO":
		return policy.IMSMediaAudio
	case "VIDEO":
		return policy.IMSMediaVideo
	case "CONTROL":
		return policy.IMSMediaControl
	default:
		return policy.IMSMediaOther
	}
}

func n5FlowUsage(u string) policy.IMSFlowUsage {
	switch strings.ToUpper(u) {
	case "RTCP":
		return policy.IMSFlowUsageRTCP
	case "AF_SIGNALLING":
		return policy.IMSFlowUsageAFSignalling
	default:
		return policy.IMSFlowUsageNoInfo
	}
}

func (s *Server) getAppSession(id string) (*appSession, bool) {
	s.appMu.Lock()
	defer s.appMu.Unlock()
	sess, ok := s.appSessions[id]
	return sess, ok
}

func writeAppSession(w http.ResponseWriter, status int, sess *appSession) {
	req := sess.Req
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(appSessionContext{
		AscReqData: &req,
		// No optional features supported yet.
		AscRespData: &appSessionContextRespData{SuppFeat: "0"},
	})
}

func writeProblem(w http.ResponseWriter, status int, cause, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"status": status, "cause": cause, "detail": detail})
}
