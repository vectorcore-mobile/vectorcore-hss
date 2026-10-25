package udm

// nudr.go — Nudr_DataManagement handlers (N36: PCF → UDR).
//
// VectorCore acts as both UDM and UDR.  The UDM interfaces (N8/N10/N13) are
// served by the nudm-* handlers.  This file serves the UDR interfaces that
// external NFs — primarily the PCF — call directly:
//
//   N36 (PCF → UDR):
//     GET /nudr-dr/v1/policy-data/ues/{ueId}/am-data
//     GET /nudr-dr/v1/policy-data/ues/{ueId}/sm-data
//     GET /nudr-dr/v1/policy-data/ues/{ueId}/ue-policy-set
//
// All responses are derived from the same subscriber / APN tables used by the
// Nudm handlers — no separate UDR database.
//
// Open5GS PCF query parameters:
//   sm-data: ?dnn=<dnn>&snssai=<json>

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

// ── AM policy data ────────────────────────────────────────────────────────────

// amPolicyData is returned to the PCF for access management policy (N36).
// Open5GS PCF uses subscribedUeAmbr and rfspIndex.
type amPolicyData struct {
	SubscribedUEAMBR *ambrData `json:"subscribedUeAmbr,omitempty"`
	RfspIndex        *int      `json:"rfspIndex,omitempty"`
	SubscCats        []string  `json:"subscCats"`
}

func (s *Server) handleUDRAMPolicyData(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveUEID(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_ueid")
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
		s.log.Error("udr: am-policy db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	resp := amPolicyData{
		SubscribedUEAMBR: &ambrData{
			Uplink:   sbi.FormatBitRate(sub.UEAMBRUp),
			Downlink: sbi.FormatBitRate(sub.UEAMBRDown),
		},
		SubscCats: []string{},
	}
	jsonOK(w, resp)
}

// ── SM policy data ────────────────────────────────────────────────────────────

// smPolicyData is one entry in the SM policy data array returned to the PCF.
type smPolicyData struct {
	SingleNSSAI     snssai           `json:"singleNssai"`
	DNN             string           `json:"dnn"`
	SessionAMBR     *ambrData        `json:"sessionAmbr,omitempty"`
	Online          *bool            `json:"online,omitempty"`
	Offline         *bool            `json:"offline,omitempty"`
	DefaultQosID    *int             `json:"defQosId,omitempty"`
	QosFlows        []qosFlowInfo    `json:"qosFlows,omitempty"`
	AllowedServices []string         `json:"allowedServices,omitempty"`
}

type qosFlowInfo struct {
	QFI int `json:"qfi"`
	QoS qosData `json:"qos"`
}

type qosData struct {
	FiveQI int `json:"5qi"`
	ARP    arp `json:"arp"`
}

type arp struct {
	PriorityLevel         int  `json:"priorityLevel"`
	PreemptCap            string `json:"preemptCap"`
	PreemptVuln           string `json:"preemptVuln"`
}

func (s *Server) handleUDRSMPolicyData(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveUEID(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_ueid")
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
		s.log.Error("udr: sm-policy db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	// Parse optional filter params from PCF.
	filterDNN := normalizeDNN(r.URL.Query().Get("dnn"))
	filterSST := 0
	if v := r.URL.Query().Get("snssai"); v != "" {
		var sn snssai
		if json.Unmarshal([]byte(v), &sn) == nil {
			filterSST = sn.SST
		}
	}

	nssaiJSON := defaultNSSAI
	if sub.NSSAI != nil && *sub.NSSAI != "" {
		nssaiJSON = *sub.NSSAI
	}
	slices := parseNSSAI(nssaiJSON)

	apnIDs := parseAPNList(sub.APNList)

	var result []smPolicyData
	for _, sl := range slices {
		if filterSST != 0 && sl.SST != filterSST {
			continue
		}
		for _, id := range apnIDs {
			apn, err := s.store.GetAPNByID(ctx, id)
			if err != nil {
				continue
			}
			if filterDNN != "" && normalizeDNN(apn.APN) != filterDNN {
				continue
			}
			offline := true
			online := false
			fiveQI := apn.QCI
			if fiveQI == 0 {
				fiveQI = 9
			}
			result = append(result, smPolicyData{
				SingleNSSAI: sl,
				DNN:         apn.APN,
				SessionAMBR: &ambrData{
					Uplink:   sbi.FormatBitRate(apn.APNAMBRUp),
					Downlink: sbi.FormatBitRate(apn.APNAMBRDown),
				},
				Online:  &online,
				Offline: &offline,
				QosFlows: []qosFlowInfo{
					{
						QFI: 1,
						QoS: qosData{
							FiveQI: fiveQI,
							ARP: arp{
								PriorityLevel: apn.ARPPriority,
								PreemptCap:    preemptCapFlag(apn.ARPPreemptionCapability),
								PreemptVuln:   preemptVulnFlag(apn.ARPPreemptionVulnerability),
							},
						},
					},
				},
			})
		}
	}
	if result == nil {
		result = []smPolicyData{}
	}
	jsonOK(w, result)
}

// ── UE policy set ─────────────────────────────────────────────────────────────

// uePolicySet is returned to the PCF for UE policy (URSP rules etc.).
// Open5GS PCF accepts an empty set — no URSP rules are required for basic 5G SA.
type uePolicySet struct {
	SubscCats   []string    `json:"subscCats"`
	UeSliceMbrs interface{} `json:"ueSliceMbrs,omitempty"`
}

func (s *Server) handleUDRUEPolicySet(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveUEID(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_ueid")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify subscriber exists.
	if _, err := s.store.GetSubscriberByIMSI(ctx, imsi); err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	} else if err != nil {
		s.log.Error("udr: ue-policy-set db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	jsonOK(w, uePolicySet{SubscCats: []string{}})
}

// ── SMS management data ───────────────────────────────────────────────────────

// smsManagementData is queried by PCF/NEF. Return basic enabled state.
type smsManagementData struct {
	MTSmsSubscribed bool `json:"mtSmsSubscribed"`
	MOSmsSubscribed bool `json:"moSmsSubscribed"`
	MTSmsBarringAll bool `json:"mtSmsBarringAll"`
	MOSmsBarringAll bool `json:"moSmsBarringAll"`
}

func (s *Server) handleUDRSMSManagement(w http.ResponseWriter, r *http.Request) {
	imsi, err := resolveUEID(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid_ueid")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if _, err := s.store.GetSubscriberByIMSI(ctx, imsi); err == repository.ErrNotFound {
		jsonError(w, http.StatusNotFound, "user_not_found")
		return
	} else if err != nil {
		s.log.Error("udr: sms-management db error", zap.String("imsi", imsi), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "db_error")
		return
	}

	jsonOK(w, smsManagementData{
		MTSmsSubscribed: true,
		MOSmsSubscribed: true,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// resolveUEID extracts the IMSI from the {ueId} path param.
// Open5GS PCF sends "imsi-{15digits}" same as SUPI.
func resolveUEID(r *http.Request) (string, error) {
	return ParseSUPI(chi.URLParam(r, "ueId"))
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
