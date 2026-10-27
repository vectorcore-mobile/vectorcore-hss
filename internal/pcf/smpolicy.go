package pcf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/policy"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

type smPolicyContextData struct {
	Supi                    string          `json:"supi"`
	Gpsi                    string          `json:"gpsi,omitempty"`
	Dnn                     string          `json:"dnn"`
	PduSessionID            int             `json:"pduSessionId,omitempty"`
	PduSessionType          string          `json:"pduSessionType,omitempty"`
	NotificationURI         string          `json:"notificationUri,omitempty"`
	SliceInfo               json.RawMessage `json:"sliceInfo,omitempty"`
	ServingNetwork          json.RawMessage `json:"servingNetwork,omitempty"`
	SubsSessAmbr            *ambr           `json:"subsSessAmbr,omitempty"`
	SubsDefQos              *qos            `json:"subsDefQos,omitempty"`
	ChargingCharacteristics string          `json:"chargingCharacteristics,omitempty"`
	SuppFeat                string          `json:"suppFeat,omitempty"`
	// UE address, used to bind N5 app sessions (TS 29.513 §5.2).
	Ipv4Address       string `json:"ipv4Address,omitempty"`
	Ipv6AddressPrefix string `json:"ipv6AddressPrefix,omitempty"`
}

type smPolicyDecision struct {
	SessRules map[string]sessionRule  `json:"sessRules,omitempty"`
	PccRules  map[string]pccRule      `json:"pccRules,omitempty"`
	QosDecs   map[string]qosDecision  `json:"qosDecs,omitempty"`
	ChgDecs   map[string]chargingDecs `json:"chgDecs,omitempty"`
	SuppFeat  string                  `json:"suppFeat,omitempty"`
}

type sessionRule struct {
	SessRuleID   string `json:"sessRuleId"`
	AuthSessAmbr *ambr  `json:"authSessAmbr,omitempty"`
	DefQos       *qos   `json:"defQos,omitempty"`
}

type ambr struct {
	Uplink   string `json:"uplink"`
	Downlink string `json:"downlink"`
}

type qos struct {
	Var5qi *int32 `json:"5qi,omitempty"`
	Arp    *arp   `json:"arp,omitempty"`
}

type arp struct {
	PriorityLevel int32  `json:"priorityLevel"`
	PreemptCap    string `json:"preemptCap"`
	PreemptVuln   string `json:"preemptVuln"`
}

type qosDecision struct {
	QosID   string `json:"qosId"`
	Var5qi  *int32 `json:"5qi,omitempty"`
	MaxbrUl string `json:"maxbrUl,omitempty"`
	MaxbrDl string `json:"maxbrDl,omitempty"`
	GbrUl   string `json:"gbrUl,omitempty"`
	GbrDl   string `json:"gbrDl,omitempty"`
	Arp     *arp   `json:"arp,omitempty"`
}

type pccRule struct {
	PccRuleID  string   `json:"pccRuleId"`
	Precedence *int32   `json:"precedence,omitempty"`
	RefQosData []string `json:"refQosData,omitempty"`
	RefChgData []string `json:"refChgData,omitempty"`
	FlowInfos  []flow   `json:"flowInfos,omitempty"`
}

type flow struct {
	FlowDescription string `json:"flowDescription,omitempty"`
	FlowDirection   string `json:"flowDirection,omitempty"`
}

type chargingDecs struct {
	ChgID                   string `json:"chgId"`
	RatingGroup             *int32 `json:"ratingGroup,omitempty"`
	ChargingCharacteristics string `json:"chargingCharacteristics,omitempty"`
}

type smPolicyAssociation struct {
	ID          string
	ResourceURI string // absolute URI returned to the SMF in Location
	Context     smPolicyContextData
	Decision    smPolicyDecision
}

type smPolicyNotification struct {
	ResourceURI      string            `json:"resourceUri"`
	SmPolicyDecision *smPolicyDecision `json:"smPolicyDecision,omitempty"`
}

type smPolicyTerminationNotification struct {
	ResourceURI string `json:"resourceUri"`
	Cause       string `json:"cause"`
}

type smPolicyUpdateData struct {
	Supi                    *string          `json:"supi,omitempty"`
	Gpsi                    *string          `json:"gpsi,omitempty"`
	Dnn                     *string          `json:"dnn,omitempty"`
	PduSessionID            *int             `json:"pduSessionId,omitempty"`
	PduSessionType          *string          `json:"pduSessionType,omitempty"`
	NotificationURI         *string          `json:"notificationUri,omitempty"`
	SliceInfo               *json.RawMessage `json:"sliceInfo,omitempty"`
	ServingNetwork          *json.RawMessage `json:"servingNetwork,omitempty"`
	SubsSessAmbr            *ambr            `json:"subsSessAmbr,omitempty"`
	SubsDefQos              *qos             `json:"subsDefQos,omitempty"`
	ChargingCharacteristics *string          `json:"chargingCharacteristics,omitempty"`
	SuppFeat                *string          `json:"suppFeat,omitempty"`
	Ipv4Address             *string          `json:"ipv4Address,omitempty"`
	Ipv6AddressPrefix       *string          `json:"ipv6AddressPrefix,omitempty"`
}

func (s *Server) registerSMPolicyRoutes(r chi.Router) {
	r.Post("/npcf-smpolicycontrol/v1/sm-policies", s.wrapOAuth("npcf-smpolicycontrol", s.handleSMPolicyCreate))
	r.Get("/npcf-smpolicycontrol/v1/sm-policies/{smPolicyId}", s.wrapOAuth("npcf-smpolicycontrol", s.handleSMPolicyGet))
	r.Post("/npcf-smpolicycontrol/v1/sm-policies/{smPolicyId}/update", s.wrapOAuth("npcf-smpolicycontrol", s.handleSMPolicyUpdate))
	r.Post("/npcf-smpolicycontrol/v1/sm-policies/{smPolicyId}/delete", s.wrapOAuth("npcf-smpolicycontrol", s.handleSMPolicyDelete))
}

func (s *Server) handleSMPolicyCreate(w http.ResponseWriter, r *http.Request) {
	var in smPolicyContextData
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, `{"cause":"INVALID_REQUEST"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Supi) == "" || strings.TrimSpace(in.Dnn) == "" || in.PduSessionID == 0 {
		http.Error(w, `{"cause":"INVALID_REQUEST"}`, http.StatusBadRequest)
		return
	}
	assoc, err := s.buildAssociation(r.Context(), in)
	if err != nil {
		if err == repository.ErrNotFound {
			http.Error(w, `{"cause":"USER_UNKNOWN"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"cause":"SYSTEM_FAILURE"}`, http.StatusInternalServerError)
		return
	}
	location := absoluteAssociationURL(r, assoc.ID)
	assoc.ResourceURI = location
	s.assocMu.Lock()
	s.associations[assoc.ID] = assoc
	s.assocMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(assoc.Decision)
}

func (s *Server) handleSMPolicyGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "smPolicyId")
	assoc, ok := s.getAssociation(id)
	if !ok {
		http.Error(w, `{"cause":"CONTEXT_NOT_FOUND"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.decisionWithAppRules(assoc))
}

func (s *Server) handleSMPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "smPolicyId")
	assoc, ok := s.getAssociation(id)
	if !ok {
		http.Error(w, `{"cause":"CONTEXT_NOT_FOUND"}`, http.StatusNotFound)
		return
	}
	nextContext, err := mergeSMPolicyContext(r.Body, assoc.Context)
	if err != nil {
		http.Error(w, `{"cause":"INVALID_REQUEST"}`, http.StatusBadRequest)
		return
	}
	refreshed, err := s.buildAssociation(r.Context(), nextContext)
	if err != nil {
		http.Error(w, `{"cause":"SYSTEM_FAILURE"}`, http.StatusInternalServerError)
		return
	}
	refreshed.ID = id
	refreshed.ResourceURI = assoc.ResourceURI
	s.assocMu.Lock()
	s.associations[id] = refreshed
	s.assocMu.Unlock()
	// The decision rebuilt from the DB carries no N5 rules; keep them so the
	// SMF's view of the session matches ours.
	decision := s.decisionWithAppRules(refreshed)
	s.notifySMPolicyUpdate(r.Context(), refreshed.Context.NotificationURI, smPolicyNotification{
		ResourceURI:      absoluteAssociationURL(r, id),
		SmPolicyDecision: &decision,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decision)
}

func (s *Server) handleSMPolicyDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "smPolicyId")
	s.assocMu.Lock()
	defer s.assocMu.Unlock()
	if _, ok := s.associations[id]; !ok {
		http.Error(w, `{"cause":"CONTEXT_NOT_FOUND"}`, http.StatusNotFound)
		return
	}
	assoc := s.associations[id]
	delete(s.associations, id)
	s.notifySMPolicyTerminate(r.Context(), assoc.Context.NotificationURI, smPolicyTerminationNotification{
		ResourceURI: absoluteAssociationURL(r, id),
		Cause:       "UNSPECIFIED",
	})
	// The PDU session is gone, so are its N5 app sessions.
	s.terminateAppSessionsFor(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) buildAssociation(ctx context.Context, in smPolicyContextData) (*smPolicyAssociation, error) {
	resolved, err := policy.ResolveSessionPolicy(ctx, s.store, in.Supi, in.Dnn)
	if err != nil {
		return nil, err
	}
	id := strconv.FormatUint(s.assocSeq.Add(1), 10)
	const defaultSessRuleID = "sess-default"
	decision := smPolicyDecision{
		SessRules: map[string]sessionRule{
			defaultSessRuleID: {
				SessRuleID:   defaultSessRuleID,
				AuthSessAmbr: chooseAMBR(in.SubsSessAmbr, &ambr{Uplink: resolved.SessionAMBRUplink, Downlink: resolved.SessionAMBRDownlink}),
				DefQos: &qos{
					Var5qi: choose5QI(in.SubsDefQos, resolved.DefaultQos5QI),
					Arp:    chooseARP(in.SubsDefQos, resolved.DefaultARP, resolved.DefaultPreemptCap, resolved.DefaultPreemptVuln),
				},
			},
		},
		PccRules: make(map[string]pccRule),
		QosDecs:  make(map[string]qosDecision),
		ChgDecs:  make(map[string]chargingDecs),
		SuppFeat: in.SuppFeat,
	}
	for _, rule := range resolved.Rules {
		var flows []flow
		for _, ruleFlow := range rule.Flows {
			flows = append(flows, flow{
				FlowDescription: ruleFlow.Description,
				FlowDirection:   flowDirection(ruleFlow.Direction),
			})
		}
		if rule.QosReference != "" {
			decision.QosDecs[rule.QosReference] = qosDecision{
				QosID:   rule.QosReference,
				Var5qi:  int32ptr(int32(rule.FiveQI)),
				MaxbrUl: rule.MaxBRUplink,
				MaxbrDl: rule.MaxBRDownlink,
				GbrUl:   rule.GuaranteedBRUL,
				GbrDl:   rule.GuaranteedBRDL,
				Arp:     newARP(rule.ARP, rule.PreemptCap, rule.PreemptVuln),
			}
		}
		pcc := pccRule{
			PccRuleID:  rule.ID,
			Precedence: int32ptr(int32(rule.Precedence)),
		}
		if len(flows) > 0 {
			pcc.FlowInfos = flows
		}
		if rule.QosReference != "" {
			pcc.RefQosData = []string{rule.QosReference}
		}
		if rule.ChargingReference != "" {
			pcc.RefChgData = []string{rule.ChargingReference}
			decision.ChgDecs[rule.ChargingReference] = chargingDecision(rule.ChargingReference, rule.RatingGroup, resolved.ChargingCharacteristics)
		}
		decision.PccRules[rule.ID] = pcc
	}
	return &smPolicyAssociation{
		ID:       id,
		Context:  in,
		Decision: decision,
	}, nil
}

func (s *Server) getAssociation(id string) (*smPolicyAssociation, bool) {
	s.assocMu.RLock()
	defer s.assocMu.RUnlock()
	assoc, ok := s.associations[id]
	return assoc, ok
}

func int32ptr(v int32) *int32 { return &v }

func absoluteAssociationURL(r *http.Request, id string) string {
	return fmt.Sprintf("%s/npcf-smpolicycontrol/v1/sm-policies/%s", requestBaseURL(r), id)
}

// requestBaseURL is the scheme://authority the request was addressed to.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	}
	return scheme + "://" + r.Host
}

func chooseAMBR(in, fallback *ambr) *ambr {
	if in != nil && (in.Uplink != "" || in.Downlink != "") {
		return in
	}
	return fallback
}

func choose5QI(in *qos, fallback int) *int32 {
	if in != nil && in.Var5qi != nil {
		return in.Var5qi
	}
	return int32ptr(int32(fallback))
}

func chooseARP(in *qos, fallback int, fallbackCap, fallbackVuln *bool) *arp {
	if in != nil && in.Arp != nil {
		out := *in.Arp
		if out.PreemptCap == "" {
			out.PreemptCap = preemptCapFlag(fallbackCap)
		}
		if out.PreemptVuln == "" {
			out.PreemptVuln = preemptVulnFlag(fallbackVuln)
		}
		return &out
	}
	return newARP(fallback, fallbackCap, fallbackVuln)
}

func newARP(priority int, cap, vuln *bool) *arp {
	return &arp{
		PriorityLevel: int32(priority),
		PreemptCap:    preemptCapFlag(cap),
		PreemptVuln:   preemptVulnFlag(vuln),
	}
}

func preemptCapFlag(b *bool) string {
	if b != nil && *b {
		return "MAY_PREEMPT"
	}
	return "NOT_PREEMPT"
}

func preemptVulnFlag(b *bool) string {
	if b != nil && *b {
		return "PREEMPTABLE"
	}
	return "NOT_PREEMPTABLE"
}

func flowDirection(direction int) string {
	switch direction {
	case 1:
		return "DOWNLINK"
	case 2:
		return "UPLINK"
	case 3:
		return "BIDIRECTIONAL"
	default:
		return "BIDIRECTIONAL"
	}
}

func chargingDecision(chgID string, ratingGroup *int, chargingCharacteristics string) chargingDecs {
	decision := chargingDecs{
		ChgID:                   chgID,
		ChargingCharacteristics: chargingCharacteristics,
	}
	if ratingGroup != nil {
		rg := int32(*ratingGroup)
		decision.RatingGroup = &rg
	}
	return decision
}

func (s *Server) notifySMPolicyUpdate(ctx context.Context, target string, payload smPolicyNotification) {
	s.notifySMPolicy(ctx, notificationTarget(target, "update"), payload)
}

func (s *Server) notifySMPolicyTerminate(ctx context.Context, target string, payload smPolicyTerminationNotification) {
	s.notifySMPolicy(ctx, notificationTarget(target, "terminate"), payload)
}

func (s *Server) notifySMPolicy(ctx context.Context, target string, payload any) {
	s.sendCallback(ctx, target, payload, "SMF", "nsmf-callback")
}

// sendCallback POSTs a JSON notification to a callback URI (SMF or AF).
func (s *Server) sendCallback(ctx context.Context, target string, payload any, targetNFType, targetService string) {
	if strings.TrimSpace(target) == "" {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		s.log.Warn("pcf: marshal notification failed", zap.String("target", target), zap.Error(err))
		return
	}
	req, err := s.sbiClient.NewRequestWithOptions(ctx, http.MethodPost, target, bytes.NewReader(raw), sbi.RequestOptions{
		RequesterNFType:       "PCF",
		RequesterNFInstanceID: s.cfg.NFInstanceID,
		TargetNFType:          targetNFType,
		TargetServiceName:     targetService,
	})
	if err != nil {
		s.log.Warn("pcf: build notification failed", zap.String("target", target), zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", sbi.FormatRequesterUserAgent("PCF", s.cfg.NFInstanceID))
	resp, err := s.sbiClient.Do(req)
	if err != nil {
		s.log.Warn("pcf: send notification failed", zap.String("target", target), zap.Error(err))
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.log.Warn("pcf: notification rejected", zap.String("target", target), zap.Int("status_code", resp.StatusCode))
	}
}

func notificationTarget(base, op string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/" + op
}

func mergeSMPolicyContext(body io.Reader, base smPolicyContextData) (smPolicyContextData, error) {
	if body == nil {
		return base, nil
	}
	var update smPolicyUpdateData
	if err := json.NewDecoder(body).Decode(&update); err != nil && err != io.EOF {
		return smPolicyContextData{}, err
	}
	return applySMPolicyUpdate(base, update), nil
}

func applySMPolicyUpdate(base smPolicyContextData, update smPolicyUpdateData) smPolicyContextData {
	out := base
	if update.Supi != nil {
		out.Supi = *update.Supi
	}
	if update.Gpsi != nil {
		out.Gpsi = *update.Gpsi
	}
	if update.Dnn != nil {
		out.Dnn = *update.Dnn
	}
	if update.PduSessionID != nil {
		out.PduSessionID = *update.PduSessionID
	}
	if update.PduSessionType != nil {
		out.PduSessionType = *update.PduSessionType
	}
	if update.NotificationURI != nil {
		out.NotificationURI = *update.NotificationURI
	}
	if update.SliceInfo != nil {
		out.SliceInfo = *update.SliceInfo
	}
	if update.ServingNetwork != nil {
		out.ServingNetwork = *update.ServingNetwork
	}
	if update.SubsSessAmbr != nil {
		out.SubsSessAmbr = update.SubsSessAmbr
	}
	if update.SubsDefQos != nil {
		out.SubsDefQos = update.SubsDefQos
	}
	if update.ChargingCharacteristics != nil {
		out.ChargingCharacteristics = *update.ChargingCharacteristics
	}
	if update.SuppFeat != nil {
		out.SuppFeat = *update.SuppFeat
	}
	if update.Ipv4Address != nil {
		out.Ipv4Address = *update.Ipv4Address
	}
	if update.Ipv6AddressPrefix != nil {
		out.Ipv6AddressPrefix = *update.Ipv6AddressPrefix
	}
	return out
}
