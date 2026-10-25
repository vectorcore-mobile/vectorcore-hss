package udm

// sdm.go — Nudm_SubscriberDataManagement handlers.
//
// GET /nudm-sdm/v{1,2}/{supi}?dataset-names=…  → multiple data sets (GetDataSets)
// GET /nudm-sdm/v{1,2}/{supi}/am-data          → AMF subscription data
// GET /nudm-sdm/v{1,2}/{supi}/sm-data          → SMF/DNN data
// GET /nudm-sdm/v{1,2}/{supi}/smf-select-data  → SMF selection info
// GET /nudm-sdm/v{1,2}/{supi}/nssai            → Allowed NSSAI
// POST/DELETE /nudm-sdm/v{1,2}/{supi}/sdm-subscriptions → change notifications (stub)
//
// AMBR format: TS 29.571 BitRate strings such as "1 Gbps", "100 Mbps".
// The DB stores AMBR in bps (the same values S6a/Gx send in Diameter AVPs);
// sbi.FormatBitRate does the conversion.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

// defaultNSSAI is returned when a subscriber has no NSSAI configured.
const defaultNSSAI = `[{"sst":1}]`

// ── GET DATA SETS ────────────────────────────────────────────────────────────

// subscriptionDataSets is the TS 29.503 SubscriptionDataSets response of
// GetDataSets. Only the data sets this UDM supports are listed.
type subscriptionDataSets struct {
	AMData     *amData                `json:"amData,omitempty"`
	SmfSelData *smfSelectData         `json:"smfSelData,omitempty"`
	UecSmfData map[string]interface{} `json:"uecSmfData,omitempty"`
	SMData     []smDataItem           `json:"smData,omitempty"`
}

// handleGetDataSets implements GetDataSets (TS 29.503):
//
//	GET /nudm-sdm/{ver}/{supi}?dataset-names=AM,SMF_SEL[,...]
//
// The Open5GS AMF uses it with AM,SMF_SEL during registration (since
// Open5GS commit e2aeae26, 2026-07); older AMFs use the per-resource
// endpoints, which return identical data via the same builders.
//
// DataSetName is an extensible enum, so names this UDM does not support are
// skipped rather than rejected. The dnn and single-nssai filters (R19) apply
// to SM only.
func (s *Server) handleGetDataSets(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveIMSI(r)
	if err != nil {
		problemJSON(w, http.StatusBadRequest, "MANDATORY_IE_INCORRECT", "invalid supi")
		return
	}

	names, detail := parseDatasetNames(r.URL.Query()["dataset-names"])
	if detail != "" {
		cause := "MANDATORY_QUERY_PARAM_INCORRECT"
		if names == nil {
			cause = "MANDATORY_QUERY_PARAM_MISSING"
		}
		problemJSON(w, http.StatusBadRequest, cause, detail)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		problemJSON(w, http.StatusNotFound, "USER_NOT_FOUND", "")
		return
	}
	if err != nil {
		s.log.Error("udm: get-data-sets db error", zap.String("imsi", imsi), zap.Error(err))
		problemJSON(w, http.StatusInternalServerError, "SYSTEM_FAILURE", "")
		return
	}

	var resp subscriptionDataSets
	var skipped []string
	for _, name := range names {
		switch name {
		case "AM":
			am := buildAMData(sub)
			resp.AMData = &am
		case "SMF_SEL":
			sel := s.buildSMFSelectData(ctx, sub)
			resp.SmfSelData = &sel
		case "UEC_SMF":
			resp.UecSmfData = buildUEContextInSMFData()
		case "SM":
			requestedDNN, filterSST := smDataFilters(r.URL.Query())
			resp.SMData = s.buildSMData(ctx, sub, requestedDNN, filterSST)
		default:
			skipped = append(skipped, name)
		}
	}

	s.log.Debug("udm: get-data-sets", zap.String("imsi", imsi),
		zap.Strings("requested", names), zap.Strings("skipped", skipped))

	jsonOK(w, resp)
}

// parseDatasetNames parses the dataset-names query parameter (form style,
// explode=false: a comma-separated list; repeated parameters are also
// accepted). TS 29.503 requires at least two unique names. It returns a
// non-empty detail on error, with names == nil when the parameter is absent.
func parseDatasetNames(values []string) (names []string, detail string) {
	if len(values) == 0 {
		return nil, "dataset-names is required"
	}
	names = []string{}
	seen := make(map[string]bool)
	for _, v := range values {
		for _, n := range strings.Split(v, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				return names, "dataset-names contains an empty name"
			}
			if seen[n] {
				return names, "dataset-names contains duplicate " + n
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) < 2 {
		return names, "dataset-names requires at least 2 names"
	}
	return names, ""
}

// ── AM-DATA ──────────────────────────────────────────────────────────────────

type amData struct {
	GPSIS                  []string      `json:"gpsis,omitempty"`
	SubscribedUEAMBR       *ambrData     `json:"subscribedUeAmbr,omitempty"`
	NSSAI                  *nssaiData    `json:"nssai,omitempty"`
	RatRestrictions        []string      `json:"ratRestrictions,omitempty"`
	ForbiddenAreas         []interface{} `json:"forbiddenAreas,omitempty"`
	ServiceAreaRestriction *struct{}     `json:"serviceAreaRestriction,omitempty"`
}

type ambrData struct {
	Uplink   string `json:"uplink"`
	Downlink string `json:"downlink"`
}

type nssaiData struct {
	DefaultSingleNssais []snssai `json:"defaultSingleNssais"`
	SingleNssais        []snssai `json:"singleNssais"`
}

type snssai struct {
	SST int    `json:"sst"`
	SD  string `json:"sd,omitempty"`
}

func (s *Server) handleAMData(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveIMSI(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_supi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	}
	if err != nil {
		s.log.Error("udm: am-data db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	jsonOK(w, buildAMData(sub))
}

// buildAMData builds AccessAndMobilitySubscriptionData for a subscriber.
// Shared by /am-data and GetDataSets (AM).
func buildAMData(sub *models.Subscriber) amData {
	slices := subscriberSlices(sub)

	resp := amData{
		SubscribedUEAMBR: &ambrData{
			Uplink:   sbi.FormatBitRate(sub.UEAMBRUp),
			Downlink: sbi.FormatBitRate(sub.UEAMBRDown),
		},
		NSSAI: &nssaiData{
			DefaultSingleNssais: slices,
			SingleNssais:        slices,
		},
	}
	if sub.MSISDN != nil && *sub.MSISDN != "" {
		resp.GPSIS = []string{"msisdn-" + *sub.MSISDN}
	}
	return resp
}

// ── SM-DATA ──────────────────────────────────────────────────────────────────

type smDataItem struct {
	SingleNssai snssai                `json:"singleNssai"`
	DNNConfigs  map[string]*dnnConfig `json:"dnnConfigurations"`
}

type dnnConfig struct {
	PDUSessionTypes pduSessionTypes `json:"pduSessionTypes"`
	SSCModes        sscModes        `json:"sscModes"`
	SessionAMBR     *ambrData       `json:"sessionAmbr,omitempty"`
	FiveGQosProfile *qosProfile     `json:"5gQosProfile,omitempty"`
}

type qosProfile struct {
	FiveQI int     `json:"5qi"`
	ARP    arpData `json:"arp"`
}

type arpData struct {
	PriorityLevel int    `json:"priorityLevel"`
	PreemptCap    string `json:"preemptCap"`
	PreemptVuln   string `json:"preemptVuln"`
}

type pduSessionTypes struct {
	DefaultSessionType  string   `json:"defaultSessionType"`
	AllowedSessionTypes []string `json:"allowedSessionTypes"`
}

type sscModes struct {
	DefaultSSCMode  string   `json:"defaultSscMode"`
	AllowedSSCModes []string `json:"allowedSscModes"`
}

func (s *Server) handleSMData(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveIMSI(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_supi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	}
	if err != nil {
		s.log.Error("udm: sm-data db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	requestedDNN, filterSST := smDataFilters(r.URL.Query())
	jsonOK(w, s.buildSMData(ctx, sub, requestedDNN, filterSST))
}

// smDataFilters extracts the optional dnn and single-nssai query filters.
func smDataFilters(q url.Values) (requestedDNN string, filterSST int) {
	requestedDNN = strings.TrimSpace(q.Get("dnn"))
	if v := q.Get("single-nssai"); v != "" {
		var sn snssai
		if json.Unmarshal([]byte(v), &sn) == nil {
			filterSST = sn.SST
		}
	}
	return requestedDNN, filterSST
}

// buildSMData builds SessionManagementSubscriptionData items, one per slice,
// each carrying all of the subscriber's DNNs. requestedDNN and filterSST
// narrow the result when non-empty/non-zero. Shared by /sm-data and
// GetDataSets (SM).
func (s *Server) buildSMData(ctx context.Context, sub *models.Subscriber, requestedDNN string, filterSST int) []smDataItem {
	slices := subscriberSlices(sub)
	filterDNN := normalizeDNN(requestedDNN)

	// Fetch APNs assigned to this subscriber.
	apnIDs := parseAPNList(sub.APNList)
	dnnCfgs := make(map[string]*dnnConfig)
	for _, id := range apnIDs {
		apn, err := s.store.GetAPNByID(ctx, id)
		if err != nil {
			continue
		}
		if filterDNN != "" && normalizeDNN(apn.APN) != filterDNN {
			continue
		}
		key := apn.APN
		if requestedDNN != "" {
			key = requestedDNN
		}
		fiveQI := apn.QCI
		if fiveQI == 0 {
			fiveQI = 9
		}
		arpPriority := apn.ARPPriority
		if arpPriority == 0 {
			arpPriority = 4
		}
		dnnCfgs[key] = &dnnConfig{
			PDUSessionTypes: pduSessionTypes{
				DefaultSessionType:  "IPV4",
				AllowedSessionTypes: []string{"IPV4"},
			},
			SSCModes: sscModes{
				DefaultSSCMode:  "SSC_MODE_1",
				AllowedSSCModes: []string{"SSC_MODE_1", "SSC_MODE_2"},
			},
			SessionAMBR: &ambrData{
				Uplink:   sbi.FormatBitRate(apn.APNAMBRUp),
				Downlink: sbi.FormatBitRate(apn.APNAMBRDown),
			},
			FiveGQosProfile: &qosProfile{
				FiveQI: fiveQI,
				ARP: arpData{
					PriorityLevel: arpPriority,
					PreemptCap:    preemptCapFlag(apn.ARPPreemptionCapability),
					PreemptVuln:   preemptVulnFlag(apn.ARPPreemptionVulnerability),
				},
			},
		}
	}

	result := []smDataItem{}
	for _, sl := range slices {
		if filterSST != 0 && sl.SST != filterSST {
			continue
		}
		result = append(result, smDataItem{
			SingleNssai: sl,
			DNNConfigs:  dnnCfgs,
		})
	}
	return result
}

// ── SMF-SELECT-DATA ──────────────────────────────────────────────────────────

type smfSelectData struct {
	SubscribedSnssaiInfos map[string]snssaiInfo `json:"subscribedSnssaiInfos,omitempty"`
}

type snssaiInfo struct {
	DNNInfos []dnnInfo `json:"dnnInfos"`
}

type dnnInfo struct {
	DNN string `json:"dnn"`
}

func formatSNSSAIKey(n snssai) string {
	if n.SD != "" {
		return fmt.Sprintf("%d-%s", n.SST, strings.ToLower(n.SD))
	}
	return strconv.Itoa(n.SST)
}

func (s *Server) handleSMFSelectData(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveIMSI(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_supi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	}
	if err != nil {
		s.log.Error("udm: smf-select db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	jsonOK(w, s.buildSMFSelectData(ctx, sub))
}

// buildSMFSelectData builds SmfSelectionSubscriptionData. The result is never
// nil: the current Open5GS AMF rejects registration when smfSelData is absent
// from a GetDataSets response. Shared by /smf-select-data and GetDataSets
// (SMF_SEL).
func (s *Server) buildSMFSelectData(ctx context.Context, sub *models.Subscriber) smfSelectData {
	apnIDs := parseAPNList(sub.APNList)
	dnns := []dnnInfo{}
	for _, id := range apnIDs {
		apn, err := s.store.GetAPNByID(ctx, id)
		if err == nil {
			dnns = append(dnns, dnnInfo{DNN: apn.APN})
		}
	}

	snssaiInfos := make(map[string]snssaiInfo)
	keys := []string{}
	for _, sl := range subscriberSlices(sub) {
		key := formatSNSSAIKey(sl)
		snssaiInfos[key] = snssaiInfo{DNNInfos: dnns}
		keys = append(keys, key)
	}

	s.log.Info("udm: smf-select-data", zap.String("imsi", sub.IMSI),
		zap.Strings("keys", keys), zap.Any("dnns", dnns))

	return smfSelectData{SubscribedSnssaiInfos: snssaiInfos}
}

// ── NSSAI ────────────────────────────────────────────────────────────────────

func (s *Server) handleNSSAI(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveIMSI(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_supi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sub, err := s.store.GetSubscriberByIMSI(ctx, imsi)
	if err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	}
	if err != nil {
		s.log.Error("udm: nssai db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	slices := subscriberSlices(sub)

	jsonOK(w, nssaiData{
		DefaultSingleNssais: slices,
		SingleNssais:        slices,
	})
}

// ── UE CONTEXT IN SMF DATA ───────────────────────────────────────────────────

// handleUEContextInSMFData returns any existing PDU sessions the UE has with
// an SMF. AMF calls this during mobility/periodic registrations to preserve
// PDU sessions. Return an empty context when there are none.
func (s *Server) handleUEContextInSMFData(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, buildUEContextInSMFData())
}

// buildUEContextInSMFData returns an empty UeContextInSmfData — no PDU
// sessions or PGW info to report. Shared by /ue-context-in-smf-data and
// GetDataSets (UEC_SMF).
func buildUEContextInSMFData() map[string]interface{} {
	return map[string]interface{}{
		"pduSessions": map[string]interface{}{},
		"pgwInfo":     []interface{}{},
	}
}

// ── SDM SUBSCRIPTIONS (stub) ─────────────────────────────────────────────────

type sdmSubscription struct {
	NfInstanceID   string   `json:"nfInstanceId"`
	CallbackURI    string   `json:"callbackReference"`
	MonitoredData  []string `json:"monitoredResourceUris,omitempty"`
	SubscriptionID string   `json:"subscriptionId,omitempty"`
}

func (s *Server) handleSDMSubscribe(w http.ResponseWriter, r *http.Request) {
	// Store subscriptions in memory only — they expire and are low-stakes.
	// Return a synthetic subscription ID.
	var sub sdmSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request")
		return
	}
	sub.SubscriptionID = "sub-" + fmt.Sprintf("%d", time.Now().UnixNano())
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	w.Header().Set("Location", scheme+"://"+r.Host+r.URL.Path+"/"+sub.SubscriptionID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(sub)
}

func (s *Server) handleSDMUnsubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func resolveIMSI(r *http.Request) (string, error) {
	return ParseSUPI(chi.URLParam(r, "supi"))
}

// subscriberSlices returns the subscriber's configured S-NSSAIs, or the
// default slice when none are configured.
func subscriberSlices(sub *models.Subscriber) []snssai {
	nssaiJSON := defaultNSSAI
	if sub.NSSAI != nil && *sub.NSSAI != "" {
		nssaiJSON = *sub.NSSAI
	}
	return parseNSSAI(nssaiJSON)
}

// parseNSSAI unmarshals a JSON NSSAI array, defaulting to SST=1 on error.
func parseNSSAI(raw string) []snssai {
	var slices []snssai
	if err := json.Unmarshal([]byte(raw), &slices); err != nil {
		return []snssai{{SST: 1}}
	}
	var filtered []snssai
	for _, sl := range slices {
		if sl.SST <= 0 {
			continue
		}
		filtered = append(filtered, sl)
	}
	if len(filtered) == 0 {
		return []snssai{{SST: 1}}
	}
	return filtered
}

// parseAPNList parses the comma-separated APN ID list stored in subscriber.apn_list.
func parseAPNList(list string) []int {
	var ids []int
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(s, "%d", &id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func normalizeDNN(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}
