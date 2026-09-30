package gx

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/diameter/avputil"
	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

func (h *Handlers) CCR(conn diam.Conn, msg *diam.Message) (*diam.Message, error) {
	var ccr CCR
	if err := msg.Unmarshal(&ccr); err != nil {
		h.log.Error("gx: CCR unmarshal failed", zap.Error(err))
		return avputil.ConstructFailureAnswer(msg, "", h.originHost, h.originRealm, diam.UnableToComply), err
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()

	sessionID := string(ccr.SessionID)
	reqType := int(ccr.CCRequestType)

	// Extract UE IP from Framed-IP-Address (4-byte big-endian OctetString).
	// Fall back to the stored serving_apn record if not present in the request (e.g. CCR-U without re-anchoring).
	ueIP := ""
	if len(ccr.FramedIPAddress) == 4 {
		ueIP = net.IP([]byte(ccr.FramedIPAddress)).String()
	}
	// Framed-IPv6-Prefix (RFC 3162) gives the PDN's IPv6 /64, the Rx binding
	// key for IPv6 (TS 29.213 §4).
	ueIPv6Prefix, _ := avputil.IPv6BindingKey(avputil.FramedIPv6PrefixRaw(msg))
	if ueIP == "" || ueIPv6Prefix == "" {
		if stored, err := h.store.GetServingAPNBySession(ctx, sessionID); err == nil {
			if ueIP == "" && stored.UEIP != nil {
				ueIP = *stored.UEIP
			}
			if ueIPv6Prefix == "" && stored.UEIPv6Prefix != nil {
				ueIPv6Prefix = *stored.UEIPv6Prefix
			}
		}
	}

	h.log.Debug("gx: CCR", zap.String("type", ccrTypeName(reqType)), zap.String("session", sessionID))

	// Build Gx CCA per RFC 4006 + 3GPP TS 29.212.
	// Gx uses Auth-Application-Id (not Auth-Session-State) to identify the application.
	ans := buildGxAnswer(msg, ccr.SessionID, h.originHost, h.originRealm)
	ans.NewAVP(avpCCRequestType, avp.Mbit, 0, datatype.Enumerated(ccr.CCRequestType))
	ans.NewAVP(avpCCRequestNumber, avp.Mbit, 0, datatype.Unsigned32(ccr.CCRequestNumber))

	// CCR-T: clear the serving APN session and any emergency session, then return.
	if reqType == CCRequestTypeTermination {
		if err := h.store.DeleteServingAPNBySession(ctx, sessionID); err != nil {
			h.log.Warn("gx: CCR-T: failed to clear serving_apn", zap.String("session", sessionID), zap.Error(err))
		} else {
			h.pub.PublishGxSessionDel(sessionID)
		}
		if termIMSI := extractIMSI(ccr.SubscriptionIDs); termIMSI != "" {
			_ = h.store.DeleteEmergencySubscriberByIMSI(ctx, termIMSI)
		}
		ans.NewAVP(avp.ResultCode, avp.Mbit, 0, datatype.Unsigned32(diam.Success))
		return ans, nil
	}

	// CCR-I or CCR-U: look up subscriber and APN, install policy.
	imsi := extractIMSI(ccr.SubscriptionIDs)
	apnName := stripAPNFQDN(string(ccr.CalledStationID))

	h.log.Debug("gx: CCR identity", zap.String("imsi", imsi), zap.String("apn", apnName))

	// Subscriber lookup (best-effort — policy still applied if not found).
	var sub *models.Subscriber
	if imsi != "" {
		var err error
		sub, err = h.store.GetSubscriberByIMSI(ctx, imsi)
		if err == repository.ErrNotFound {
			h.log.Warn("gx: CCR unknown IMSI", zap.String("imsi", imsi))
		} else if err != nil {
			h.log.Error("gx: CCR subscriber lookup failed", zap.Error(err))
		}
	}

	// If the subscriber is unknown and this is a CCR-I, record an emergency session.
	// This covers the case where the MME permitted an emergency attach for an
	// unprovisioned UE and the PGW now establishes a bearer.
	if sub == nil && imsi != "" && reqType == CCRequestTypeInitial {
		now := time.Now().UTC()
		nowStr := now.Format(time.RFC3339)
		pgwHost := string(ccr.OriginHost)
		pgwRealm := string(ccr.OriginRealm)
		ratStr := ratTypeName(int(ccr.RATType))
		rec := &models.EmergencySubscriber{
			IMSI:                &imsi,
			ServingPGW:          &pgwHost,
			ServingPGWTimestamp: &nowStr,
			GxOriginHost:        &pgwHost,
			GxOriginRealm:       &pgwRealm,
			RATType:             &ratStr,
			LastModified:        nowStr,
		}
		if ueIP != "" {
			rec.IP = &ueIP
		}
		if err := h.store.UpsertEmergencySubscriber(ctx, rec); err != nil {
			h.log.Error("gx: CCR failed to write emergency_subscriber", zap.String("imsi", imsi), zap.Error(err))
		} else {
			h.log.Info("gx: emergency session created", zap.String("imsi", imsi), zap.String("pgw", pgwHost))
		}
	}

	// APN lookup.
	apnRecord, err := h.store.GetAPNByName(ctx, apnName)
	if err == repository.ErrNotFound {
		h.log.Warn("gx: CCR unknown APN", zap.String("apn", apnName))
	} else if err != nil {
		h.log.Error("gx: CCR APN lookup failed", zap.Error(err))
	}

	// Static IP assignment — subscriber_routing overrides the PGW-assigned UE IP.
	// Only applied on CCR-I when both subscriber and APN are known.
	if reqType == CCRequestTypeInitial && sub != nil && apnRecord != nil {
		if routing, err := h.store.GetSubscriberRoutingBySubscriberAndAPN(ctx, sub.SubscriberID, apnRecord.APNID); err == nil && routing.IPAddress != nil {
			if ip := net.ParseIP(*routing.IPAddress); ip != nil {
				if ip4 := ip.To4(); ip4 != nil {
					ueIP = ip4.String()
					ans.NewAVP(avp.FramedIPAddress, avp.Mbit, 0, datatype.OctetString(ip4))
					h.log.Debug("gx: CCR static IP assigned",
						zap.String("imsi", imsi),
						zap.String("apn", apnName),
						zap.String("ip", ueIP),
					)
				}
			}
		}
	}

	// Charging rules — filter by APN's charging_rule_list if configured.
	rules, err := h.resolveChargingRules(ctx, apnRecord)
	if err != nil {
		h.log.Error("gx: CCR failed to resolve charging rules", zap.Error(err))
	}

	// Supported-Features — must be echoed per 3GPP TS 29.212 §4.5.
	// Without this the P-GW treats the PCRF as feature-unsupported and stops
	// sending CCR-I for subsequent attach attempts.
	ans.NewAVP(avp.SupportedFeatures, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avp.VendorID, avp.Mbit, 0, datatype.Unsigned32(Vendor3GPP)),
		diam.NewAVP(avp.FeatureListID, avp.Vbit, Vendor3GPP, datatype.Unsigned32(1)),
		diam.NewAVP(avp.FeatureList, avp.Vbit, Vendor3GPP, datatype.Unsigned32(11)), // Rel8+Rel9+RuleVersioning
	}})

	// Default-EPS-Bearer-QoS from APN QCI/ARP.
	if apnRecord != nil {
		ans.NewAVP(avpDefaultEPSBearerQoS, avp.Vbit, Vendor3GPP, buildDefaultBearerQoS(apnRecord))
	}

	// QoS-Information (APN-AMBR) — required by P-GW to set the aggregate MBR on the PDN connection.
	// Per PyHSS reference: top-level APN-AMBR QoS-Information and sub-AVPs use V-bit only (no M-bit).
	if apnRecord != nil && (apnRecord.APNAMBRDown > 0 || apnRecord.APNAMBRUp > 0) {
		ans.NewAVP(avpQoSInformation, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpAPNAggMaxBRUL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(apnRecord.APNAMBRUp))),
			diam.NewAVP(avpAPNAggMaxBRDL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(apnRecord.APNAMBRDown))),
		}})
	}

	// Charging-Rule-Install with full Charging-Rule-Definition per rule.
	for _, rule := range rules {
		var tfts []models.TFT
		if rule.TFTGroupID != nil {
			tfts, _ = h.store.GetTFTsByGroupID(ctx, *rule.TFTGroupID)
		}
		ruleAVP := buildChargingRuleDefinition(rule, tfts, ueIP, h.tftHandling, h.log)
		ans.NewAVP(avpChargingRuleInstall, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{
			AVP: []*diam.AVP{ruleAVP},
		})
	}

	// Track session in serving_apn on CCR-I and CCR-U.
	// Store even when apnRecord is nil (APN not configured in DB) — subscriber presence is enough.
	if sub != nil && (reqType == CCRequestTypeInitial || reqType == CCRequestTypeUpdate) {
		now := time.Now().UTC()
		pgw := conn.RemoteAddr().String()
		pgwHost := string(ccr.OriginHost)
		pgwRealm := string(ccr.OriginRealm)
		var ueIPPtr *string
		if ueIP != "" {
			ueIPPtr = &ueIP
		}
		var ueIPv6PrefixPtr *string
		if ueIPv6Prefix != "" {
			ueIPv6PrefixPtr = &ueIPv6Prefix
		}
		apnID := 0
		ipVersion := 0
		if apnRecord != nil {
			apnID = apnRecord.APNID
			ipVersion = apnRecord.IPVersion
		}
		record := &models.ServingAPN{
			SubscriberID:        sub.SubscriberID,
			APNID:               apnID,
			APNName:             apnName,
			PCRFSessionID:       &sessionID,
			IPVersion:           ipVersion,
			UEIP:                ueIPPtr,
			UEIPv6Prefix:        ueIPv6PrefixPtr,
			ServingPGW:          &pgw,
			ServingPGWTimestamp: &now,
			ServingPGWRealm:     &pgwRealm,
			ServingPGWPeer:      &pgwHost,
			LastModified:        now.Format(time.RFC3339),
		}
		if err := h.store.UpsertServingAPN(ctx, record); err != nil {
			h.log.Error("gx: CCR failed to upsert serving_apn", zap.Error(err))
		} else if reqType == CCRequestTypeInitial {
			apnIDPtr := &apnID
			apnNamePtr := &apnName
			h.pub.PublishGxSessionAdd(geored.PayloadGxSessionAdd{
				PCRFSessionID: sessionID,
				IMSI:          imsi,
				MSISDN:        sub.MSISDN,
				APNID:         apnIDPtr,
				APNName:       apnNamePtr,
				PGWIP:         record.ServingPGW,
				UEIP:          ueIPPtr,
			})
		}
	}

	// Result-Code goes last, matching PyHSS behaviour.
	ans.NewAVP(avp.ResultCode, avp.Mbit, 0, datatype.Unsigned32(diam.Success))

	h.log.Debug("gx: CCA sent",
		zap.String("type", ccrTypeName(reqType)),
		zap.String("apn", apnName),
		zap.Int("rules_installed", len(rules)),
	)
	return ans, nil
}

// resolveChargingRules returns the rules applicable to an APN.
// If the APN has a charging_rule_list configured, only those named rules are
// returned. If charging_rule_list is absent or empty, no rules are returned —
// the P-GW uses the subscribed QoS from the ULA. This is the correct behaviour
// for IMS APNs (QCI=5): bearer policy for voice/video is installed later via Rx
// when a call is established, not at initial bearer activation.
func (h *Handlers) resolveChargingRules(ctx context.Context, apnRecord *models.APN) ([]models.ChargingRule, error) {
	if apnRecord == nil || apnRecord.ChargingRuleList == nil || *apnRecord.ChargingRuleList == "" {
		return nil, nil
	}
	var ids []int
	for _, s := range splitTrim(*apnRecord.ChargingRuleList) {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return h.store.GetChargingRulesByIDs(ctx, ids)
}

// buildDefaultBearerQoS builds the Default-EPS-Bearer-QoS AVP from APN fields.
func buildDefaultBearerQoS(a *models.APN) *diam.GroupedAVP {
	cap_ := uint32(1) // PRE-EMPTION_CAPABILITY_DISABLED
	vuln := uint32(0) // PRE-EMPTION_VULNERABILITY_ENABLED
	if a.ARPPreemptionCapability != nil && *a.ARPPreemptionCapability {
		cap_ = 0
	}
	if a.ARPPreemptionVulnerability != nil && !*a.ARPPreemptionVulnerability {
		vuln = 1
	}

	qci := uint32(9)
	if a.QCI != 0 {
		qci = uint32(a.QCI)
	}
	prio := uint32(4)
	if a.ARPPriority != 0 {
		prio = uint32(a.ARPPriority)
	}

	return &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avpQoSClassIdentifier, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(qci)),
		diam.NewAVP(avpAllocationRetentionPri, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpPriorityLevel, avp.Vbit, Vendor3GPP, datatype.Unsigned32(prio)),
			diam.NewAVP(avpPreemptionCapability, avp.Vbit, Vendor3GPP, datatype.Enumerated(cap_)),
			diam.NewAVP(avpPreemptionVulnerability, avp.Vbit, Vendor3GPP, datatype.Enumerated(vuln)),
		}}),
	}}
}

// buildChargingRuleDefinition builds a full Charging-Rule-Definition AVP from a
// ChargingRule model including QoS parameters, MBR/GBR, precedence, and TFT flow filters.
func buildChargingRuleDefinition(rule models.ChargingRule, tfts []models.TFT, ueIP string, tftHandling string, log *zap.Logger) *diam.AVP {
	if log == nil {
		log = zap.NewNop()
	}

	cap_ := uint32(1)
	vuln := uint32(0)
	if rule.ARPPreemptionCapability != nil && *rule.ARPPreemptionCapability {
		cap_ = 0
	}
	if rule.ARPPreemptionVulnerability != nil && !*rule.ARPPreemptionVulnerability {
		vuln = 1
	}

	qci := uint32(9)
	if rule.QCI != 0 {
		qci = uint32(rule.QCI)
	}
	prio := uint32(4)
	if rule.ARPPriority != 0 {
		prio = uint32(rule.ARPPriority)
	}

	qosAVPs := []*diam.AVP{
		diam.NewAVP(avpQoSClassIdentifier, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(qci)),
		diam.NewAVP(avpMaxReqBWDL, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(rule.MBRDown))),
		diam.NewAVP(avpMaxReqBWUL, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(rule.MBRUp))),
		diam.NewAVP(avpAllocationRetentionPri, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpPriorityLevel, avp.Vbit, Vendor3GPP, datatype.Unsigned32(prio)),
			diam.NewAVP(avpPreemptionCapability, avp.Vbit, Vendor3GPP, datatype.Enumerated(cap_)),
			diam.NewAVP(avpPreemptionVulnerability, avp.Vbit, Vendor3GPP, datatype.Enumerated(vuln)),
		}}),
	}
	// GBR only applies to QCI 1-4 (GBR bearers). QCI 5-9 are non-GBR per 3GPP TS 23.203.
	if rule.GBRDown > 0 && qci <= 4 {
		qosAVPs = append(qosAVPs,
			diam.NewAVP(avpGuaranteedBitrateDL, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(rule.GBRDown))),
			diam.NewAVP(avpGuaranteedBitrateUL, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(rule.GBRUp))),
		)
	}

	defAVPs := []*diam.AVP{
		diam.NewAVP(avpChargingRuleName, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(rule.RuleName)),
	}

	// Flow-Information (TFT): one AVP per TFT entry.
	for _, tft := range tfts {
		tftStr := strings.ReplaceAll(tft.TFTString, "{{UE_IP}}", ueIP)
		tftStr = strings.ReplaceAll(tftStr, "{UE_IP}", ueIP)
		effectiveTFT, rewritten := ApplyTFTHandling(tftStr, tftHandling)
		if rewritten {
			log.Debug("TFT rewrite applied: mode=flip-permit-in original=\"" + tftStr + "\" effective=\"" + effectiveTFT + "\"")
		} else if shouldRewritePermitInTFT(tftStr, tftHandling) {
			log.Warn("Unable to rewrite malformed permit-in TFT; passing unchanged: \"" + tftStr + "\"")
		}
		tftStr = effectiveTFT
		defAVPs = append(defAVPs, diam.NewAVP(avpFlowInformation, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpFlowDirection, avp.Vbit, Vendor3GPP, datatype.Enumerated(tft.Direction)),
			diam.NewAVP(avpFlowDescription, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.IPFilterRule(tftStr)),
		}}))
	}

	// Flow-Status: ENABLED (2).
	defAVPs = append(defAVPs, diam.NewAVP(avpFlowStatus, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(2)))

	defAVPs = append(defAVPs, diam.NewAVP(avpQoSInformation, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: qosAVPs}))

	if rule.Precedence != nil {
		defAVPs = append(defAVPs,
			diam.NewAVP(avpPrecedence, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(uint32(*rule.Precedence))),
		)
	}
	if rule.RatingGroup != nil {
		defAVPs = append(defAVPs,
			diam.NewAVP(avp.RatingGroup, avp.Mbit, 0, datatype.Unsigned32(uint32(*rule.RatingGroup))),
		)
	}

	return diam.NewAVP(avpChargingRuleDefinition, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: defAVPs})
}

func ApplyTFTHandling(tft string, mode string) (string, bool) {
	if !shouldRewritePermitInTFT(tft, mode) {
		return tft, false
	}

	tokens := strings.Fields(tft)
	if len(tokens) < 7 ||
		!strings.EqualFold(tokens[0], "permit") ||
		!strings.EqualFold(tokens[1], "in") ||
		!strings.EqualFold(tokens[3], "from") {
		return tft, false
	}

	toIdx := -1
	for i := 5; i < len(tokens); i++ {
		if strings.EqualFold(tokens[i], "to") {
			toIdx = i
			break
		}
	}
	if toIdx == -1 || toIdx == 4 || toIdx == len(tokens)-1 {
		return tft, false
	}

	optionIdx := len(tokens)
	for i := toIdx + 2; i < len(tokens); i++ {
		if isIPFilterRuleOption(tokens[i]) {
			optionIdx = i
			break
		}
	}
	if optionIdx == toIdx+1 {
		return tft, false
	}

	src := tokens[4:toIdx]
	dst := tokens[toIdx+1 : optionIdx]
	rewritten := []string{
		"permit", "out", tokens[2],
		"from",
	}
	rewritten = append(rewritten, dst...)
	rewritten = append(rewritten, "to")
	rewritten = append(rewritten, src...)
	rewritten = append(rewritten, tokens[optionIdx:]...)
	return strings.Join(rewritten, " "), true
}

func shouldRewritePermitInTFT(tft string, mode string) bool {
	if mode != "flip-permit-in" {
		return false
	}
	tokens := strings.Fields(tft)
	return len(tokens) >= 2 &&
		strings.EqualFold(tokens[0], "permit") &&
		strings.EqualFold(tokens[1], "in")
}

func isIPFilterRuleOption(token string) bool {
	switch strings.ToLower(token) {
	case "frag", "ipoptions", "tcpoptions", "established", "setup", "tcpflags", "icmptypes":
		return true
	default:
		return false
	}
}

// extractIMSI finds the IMSI from a Subscription-Id list (Type=1 END_USER_IMSI).
func extractIMSI(ids []SubscriptionID) string {
	for _, id := range ids {
		if int(id.Type) == SubscriptionIDTypeIMSI {
			return string(id.Data)
		}
	}
	// Fall back to first entry if no IMSI type found.
	if len(ids) > 0 {
		return string(ids[0].Data)
	}
	return ""
}

// stripAPNFQDN strips the 3GPP FQDN suffix from a Called-Station-Id.
// "internet.mnc001.mcc001.gprs" → "internet"
// "internet" → "internet"
func stripAPNFQDN(calledStationID string) string {
	if idx := strings.IndexByte(calledStationID, '.'); idx > 0 {
		return calledStationID[:idx]
	}
	return calledStationID
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// buildGxAnswer constructs a bare Gx CCA frame with Session-Id, Origin-Host/Realm,
// and Auth-Application-Id (required by RFC 4006). Result-Code is NOT added here —
// callers add it last, matching the PyHSS AVP ordering that the P-GW expects.
func buildGxAnswer(req *diam.Message, sessionID datatype.UTF8String, originHost, originRealm string) *diam.Message {
	ans := diam.NewMessage(req.Header.CommandCode, req.Header.CommandFlags&^diam.RequestFlag, AppIDGx, req.Header.HopByHopID, req.Header.EndToEndID, req.Dictionary())
	ans.InsertAVP(diam.NewAVP(avp.SessionID, avp.Mbit, 0, sessionID))
	ans.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(originHost))
	ans.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(originRealm))
	ans.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(AppIDGx))
	return ans
}

func ratTypeName(t int) string {
	switch t {
	case 1000:
		return "WLAN"
	case 1001:
		return "VIRTUAL"
	case 1002:
		return "UTRAN"
	case 1003:
		return "GERAN"
	case 1004:
		return "EUTRAN"
	case 1005:
		return "EUTRAN-NB-IoT"
	case 1006:
		return "NR"
	default:
		return "UNKNOWN"
	}
}

func ccrTypeName(t int) string {
	switch t {
	case CCRequestTypeInitial:
		return "CCR-I"
	case CCRequestTypeUpdate:
		return "CCR-U"
	case CCRequestTypeTermination:
		return "CCR-T"
	default:
		return "CCR-?"
	}
}
