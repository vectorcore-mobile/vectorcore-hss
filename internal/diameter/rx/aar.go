package rx

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/diameter/avputil"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// AAR handles AA-Request from the P-CSCF (command 265).
// It installs dedicated bearer rules on the PGW via a Gx RAR, then returns AAA.
func (h *Handlers) AAR(conn diam.Conn, msg *diam.Message) (*diam.Message, error) {
	var aar AAR
	if err := msg.Unmarshal(&aar); err != nil {
		h.log.Error("rx: AAR unmarshal failed", zap.Error(err))
		return avputil.ConstructFailureAnswer(msg, "", h.originHost, h.originRealm, diam.UnableToComply), err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessionID := string(aar.SessionID)
	identity := extractSubscriberID(aar.SubscriptionIDs)

	h.log.Debug("rx: AAR", zap.String("session", sessionID), zap.String("subscriber", identity),
		zap.Int("media_components", len(aar.MediaComponents)))

	// Bind to the Gx session owning the AAR's UE IP (TS 29.213 §4). The
	// P-CSCF may send any Subscription-ID-Type (IMSI, E164, or SIP-URI) and
	// the local part of a SIP URI may be either the IMSI or the MSISDN; the
	// identity-only lookup is the fallback when no UE IP matches.
	gxRec, binding, err := bindRxToGxSession(ctx, h.store, identity, []byte(aar.FramedIPAddress), avputil.FramedIPv6PrefixRaw(msg))
	if err == repository.ErrNotFound {
		h.log.Warn("rx: AAR no active Gx session for subscriber", zap.String("subscriber", identity))
		// Return success anyway — voice call can still proceed without dedicated bearer.
		return buildRxAnswer(msg, aar.SessionID, h.originHost, h.originRealm), nil
	}
	if err != nil {
		h.log.Error("rx: AAR Gx session lookup failed", zap.String("subscriber", identity), zap.Error(err))
		return buildRxAnswer(msg, aar.SessionID, h.originHost, h.originRealm), nil
	}

	if gxRec.PCRFSessionID == nil || gxRec.ServingPGWPeer == nil {
		h.log.Warn("rx: AAR Gx session has no PCRF session or PGW peer", zap.String("subscriber", identity))
		return buildRxAnswer(msg, aar.SessionID, h.originHost, h.originRealm), nil
	}

	h.log.Debug("rx: AAR bound to Gx session", zap.String("subscriber", identity),
		zap.String("binding", binding), zap.String("gx_session", *gxRec.PCRFSessionID),
		zap.String("apn", gxRec.APNName))

	pgwPeer := *gxRec.ServingPGWPeer
	pgwRealm := ""
	if gxRec.ServingPGWRealm != nil {
		pgwRealm = *gxRec.ServingPGWRealm
	}

	// Build one bearer rule per qualifying media component, then install all
	// of them in a single Gx RAR. Sending one RAR per rule would result in
	// multiple simultaneous requests on the same session, which many DRAs and
	// PGWs reject with DIAMETER_UNABLE_TO_DELIVER (3002).
	type bearerRule struct {
		name          string
		qci           uint32
		bwDL          uint32
		bwUL          uint32
		gbr           bool
		arpPriority   uint32
		precedence    uint32
		subComponents [][]string // per sub-component: list of Flow-Description strings
	}
	var rules []bearerRule
	for _, mc := range aar.MediaComponents {
		qci, gbr, defaultBW, arpPrio, prec := mediaTypeQoSDefaults(int(mc.MediaType))
		if qci == 0 {
			continue
		}
		bwDL := uint32(mc.MaxRequestedBWDL)
		bwUL := uint32(mc.MaxRequestedBWUL)
		if bwDL == 0 {
			bwDL = uint32(mc.RRBandwidth) + uint32(mc.RSBandwidth)
		}
		if bwUL == 0 {
			bwUL = uint32(mc.RRBandwidth) + uint32(mc.RSBandwidth)
		}
		// Apply default bandwidth if the P-CSCF provided nothing.
		if bwDL == 0 {
			bwDL = defaultBW
		}
		if bwUL == 0 {
			bwUL = defaultBW
		}
		// Accept P-CSCF-requested bandwidth only if it is not higher than the default.
		if bwDL > defaultBW {
			bwDL = defaultBW
		}
		if bwUL > defaultBW {
			bwUL = defaultBW
		}
		var subComponents [][]string
		for _, sc := range mc.MediaSubComponents {
			var fds []string
			for _, fd := range sc.FlowDescriptions {
				if s := string(fd); s != "" {
					fds = append(fds, s)
				}
			}
			if len(fds) > 0 {
				subComponents = append(subComponents, fds)
			}
		}
		rules = append(rules, bearerRule{
			name:          rxChargingRuleName(sessionID, uint32(mc.MediaComponentNumber)),
			qci:           uint32(qci),
			bwDL:          bwDL,
			bwUL:          bwUL,
			gbr:           gbr,
			arpPriority:   arpPrio,
			precedence:    prec,
			subComponents: subComponents,
		})
	}

	var ruleNames []string
	if len(rules) > 0 {
		// Build the grouped Charging-Rule-Definition AVPs for a single RAR.
		// AVP ordering follows 3GPP TS 29.212 and PyHSS reference:
		//   Charging-Rule-Name → Flow-Information(s) → Flow-Status → QoS-Information → Precedence
		var ruleDefs []*diam.AVP
		for _, r := range rules {
			// Flow-Information: one AVP per flow description.
			var flowAVPs []*diam.AVP
			for _, sc := range r.subComponents {
				for _, fd := range sc {
					flowAVPs = append(flowAVPs, h.buildRxFlowInformation(fd))
				}
			}

			// QoS-Information for this bearer.
			// ARP Pre-Emption-Capability = DISABLED (1): voice/video bearers must not preempt.
			// ARP Pre-Emption-Vulnerability = ENABLED (0): these bearers may be preempted.
			qosAVPs := []*diam.AVP{
				diam.NewAVP(avpGxQoSClassIdentifier, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(r.qci)),
				diam.NewAVP(avpGxAllocationRetentionPri, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{
					diam.NewAVP(avpGxPriorityLevel, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.arpPriority)),
					diam.NewAVP(avpGxPreemptionCapability, avp.Vbit, Vendor3GPP, datatype.Enumerated(preemptionCapabilityDisabled)),
					diam.NewAVP(avpGxPreemptionVulnerability, avp.Vbit, Vendor3GPP, datatype.Enumerated(preemptionVulnerabilityEnabled)),
				}}),
				diam.NewAVP(avpMaxRequestedBWDL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.bwDL)),
				diam.NewAVP(avpMaxRequestedBWUL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.bwUL)),
			}
			if r.gbr {
				qosAVPs = append(qosAVPs,
					diam.NewAVP(avpGxGuaranteedBitrateDL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.bwDL)),
					diam.NewAVP(avpGxGuaranteedBitrateUL, avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.bwUL)),
				)
			}

			// Assemble the rule definition in spec order.
			ruleAVPs := []*diam.AVP{
				diam.NewAVP(avpGxChargingRuleName, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(r.name)),
			}
			ruleAVPs = append(ruleAVPs, flowAVPs...)
			ruleAVPs = append(ruleAVPs,
				diam.NewAVP(avpGxFlowStatus, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Enumerated(2)), // ENABLED
				diam.NewAVP(avpGxQoSInformation, avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: qosAVPs}),
				diam.NewAVP(avpGxPrecedence, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.Unsigned32(r.precedence)),
			)

			ruleDefs = append(ruleDefs, diam.NewAVP(avpGxChargingRuleDefinition, avp.Mbit|avp.Vbit, Vendor3GPP,
				&diam.GroupedAVP{AVP: ruleAVPs},
			))
			ruleNames = append(ruleNames, r.name)
		}

		if err := h.sendGxRAR(pgwPeer, pgwRealm, *gxRec.PCRFSessionID, ruleDefs); err != nil {
			h.log.Error("rx: AAR Gx RAR failed", zap.String("subscriber", identity), zap.Error(err))
			ruleNames = nil
		} else {
			h.log.Debug("rx: AAR dedicated bearers installed",
				zap.String("subscriber", identity), zap.Strings("rules", ruleNames))
		}
	}

	// Store Rx session for cleanup on STR.
	h.mu.Lock()
	h.sessions[sessionID] = &rxSession{
		imsi:        identity,
		gxSessionID: *gxRec.PCRFSessionID,
		pgwPeer:     pgwPeer,
		pgwRealm:    pgwRealm,
		ruleNames:   ruleNames,
	}
	h.mu.Unlock()

	h.log.Debug("rx: AAR success", zap.String("subscriber", identity), zap.Int("rules", len(ruleNames)))
	return buildRxAnswer(msg, aar.SessionID, h.originHost, h.originRealm), nil
}

func (h *Handlers) buildRxFlowInformation(fd string) *diam.AVP {
	dir := detectFlowDirectionFromRxFlowDescription(fd)

	effectiveFD, rewritten := h.applyTFTHandling(fd)
	if rewritten {
		h.log.Debug("rx: TFT rewrite applied", zap.String("original", fd), zap.String("effective", effectiveFD))
	}

	return diam.NewAVP(avpGxFlowInformation, avp.Vbit, Vendor3GPP,
		&diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avpFlowDescription, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.IPFilterRule(effectiveFD)),
			diam.NewAVP(avpGxFlowDirection, avp.Vbit, Vendor3GPP, datatype.Enumerated(dir)),
		}},
	)
}

func detectFlowDirectionFromRxFlowDescription(fd string) uint32 {
	lower := strings.ToLower(strings.TrimSpace(fd))
	switch {
	case strings.HasPrefix(lower, "permit out"):
		return flowDirectionDownlink
	case strings.HasPrefix(lower, "permit in"):
		return flowDirectionUplink
	default:
		return flowDirectionBidirectional
	}
}

// STR handles Session-Termination-Request from the P-CSCF (command 275).
// It removes the dedicated bearer rules installed during AAR via a Gx RAR.
func (h *Handlers) STR(conn diam.Conn, msg *diam.Message) (*diam.Message, error) {
	var str STR
	if err := msg.Unmarshal(&str); err != nil {
		h.log.Error("rx: STR unmarshal failed", zap.Error(err))
		return avputil.ConstructFailureAnswer(msg, "", h.originHost, h.originRealm, diam.UnableToComply), err
	}

	sessionID := string(str.SessionID)
	h.log.Debug("rx: STR", zap.String("session", sessionID))

	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	if ok {
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()

	if ok && len(sess.ruleNames) > 0 {
		if err := h.sendGxRARRemove(sess.pgwPeer, sess.pgwRealm, sess.gxSessionID, sess.ruleNames); err != nil {
			h.log.Error("rx: STR Gx RAR remove failed",
				zap.String("session", sessionID), zap.Error(err))
		} else {
			h.log.Debug("rx: STR dedicated bearers removed",
				zap.String("imsi", sess.imsi), zap.Strings("rules", sess.ruleNames))
		}
	}

	h.log.Debug("rx: STR success", zap.String("session", sessionID))
	return buildRxAnswer(msg, str.SessionID, h.originHost, h.originRealm), nil
}

// sendGxRAR sends a single Re-Auth-Request to the PGW containing all provided
// Charging-Rule-Definition AVPs inside one Charging-Rule-Install group.
// Callers must build all rule definitions before calling this so that every
// bearer for a given AAR is installed in one Diameter transaction.
func (h *Handlers) sendGxRAR(pgwPeer, pgwRealm, gxSessionID string, ruleDefs []*diam.AVP) error {
	conn, ok := h.peers.GetConn(pgwPeer)
	if !ok {
		return fmt.Errorf("PGW peer %q not connected", pgwPeer)
	}

	msg := diam.NewRequest(cmdRAR, appIDGx, nil)
	msg.Header.CommandFlags |= diam.ProxiableFlag
	msg.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(gxSessionID))
	msg.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(appIDGx))
	msg.NewAVP(avp.ReAuthRequestType, avp.Mbit, 0, datatype.Enumerated(reAuthRequestTypeAuthorizeOnly))
	msg.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(h.originHost))
	msg.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(h.originRealm))
	msg.NewAVP(avp.DestinationHost, avp.Mbit, 0, datatype.DiameterIdentity(pgwPeer))
	if pgwRealm != "" {
		msg.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity(pgwRealm))
	}
	msg.NewAVP(avpGxChargingRuleInstall, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: ruleDefs})

	_, err := msg.WriteTo(conn)
	return err
}

// sendGxRARRemove sends a Re-Auth-Request to the PGW to remove dedicated bearer rules.
func (h *Handlers) sendGxRARRemove(pgwPeer, pgwRealm, gxSessionID string, ruleNames []string) error {
	conn, ok := h.peers.GetConn(pgwPeer)
	if !ok {
		return fmt.Errorf("PGW peer %q not connected", pgwPeer)
	}

	msg := diam.NewRequest(cmdRAR, appIDGx, nil)
	msg.Header.CommandFlags |= diam.ProxiableFlag
	msg.NewAVP(avp.SessionID, avp.Mbit, 0, datatype.UTF8String(gxSessionID))
	msg.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(appIDGx))
	msg.NewAVP(avp.ReAuthRequestType, avp.Mbit, 0, datatype.Enumerated(reAuthRequestTypeAuthorizeOnly))
	msg.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(h.originHost))
	msg.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(h.originRealm))
	msg.NewAVP(avp.DestinationHost, avp.Mbit, 0, datatype.DiameterIdentity(pgwPeer))
	if pgwRealm != "" {
		msg.NewAVP(avp.DestinationRealm, avp.Mbit, 0, datatype.DiameterIdentity(pgwRealm))
	}

	removeAVPs := make([]*diam.AVP, 0, len(ruleNames))
	for _, name := range ruleNames {
		removeAVPs = append(removeAVPs,
			diam.NewAVP(avpGxChargingRuleName, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.OctetString(name)),
		)
	}
	msg.NewAVP(avpGxChargingRuleRemove, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: removeAVPs})

	_, err := msg.WriteTo(conn)
	return err
}

// mediaTypeQoSDefaults maps an Rx Media-Type to QoS parameters for the Gx RAR.
//
// Returns:
//
//	qci        — Gx QCI value (0 = no dedicated bearer for this type)
//	gbr        — true if a GBR bearer is required (QCI 1–4)
//	defaultBW  — default MBR/GBR in bps, applied when the P-CSCF omits bandwidth AVPs
//	arpPriority — ARP Priority-Level per 3GPP TS 23.203 Table 6.1.7
//	precedence  — charging rule precedence (lower = higher priority)
//
// ARP values are per 3GPP TS 23.203 §6.1.7 QCI characteristics:
//
//	QCI 1 (voice): priority 2 → use 14 per common operator practice
//	QCI 2 (video): priority 4 → use 11 per common operator practice
//	QCI 5 (IMS signalling): priority 1 → use 1
//
// Pre-Emption-Capability is always DISABLED and Pre-Emption-Vulnerability is
// always ENABLED for these dedicated IMS bearers (set at call site).
func mediaTypeQoSDefaults(mediaType int) (qci int, gbr bool, defaultBW, arpPriority, precedence uint32) {
	switch mediaType {
	case MediaTypeAudio:
		return 1, true, 128000, 14, 40 // QCI 1 — GBR VoLTE voice, 128 kbps
	case MediaTypeVideo:
		return 2, true, 512000, 11, 30 // QCI 2 — GBR VoLTE video, 512 kbps
	case MediaTypeControl:
		return 5, false, 64000, 1, 50 // QCI 5 — non-GBR IMS signalling
	default:
		return 0, false, 0, 0, 0 // no dedicated bearer
	}
}

// extractSubscriberID returns the best subscriber identity string from the
// Subscription-Id AVP list. Priority: IMSI > E164/MSISDN > SIP-URI.
//
// For SIP-URI the local part is extracted (strip "sip:"/"tel:" and "@domain").
// A leading "+" is stripped from all types so that E.164 values stored without
// a country-code prefix (e.g. "3342012832") still match values sent as
// "+13342012832" after the caller normalises country codes separately.
func extractSubscriberID(ids []SubscriptionID) string {
	clean := func(s string) string {
		return strings.TrimPrefix(s, "+")
	}
	for _, id := range ids {
		if int(id.Type) == SubscriptionIDTypeIMSI {
			return clean(string(id.Data))
		}
	}
	for _, id := range ids {
		if int(id.Type) == SubscriptionIDTypeMSISDN {
			return clean(string(id.Data))
		}
	}
	for _, id := range ids {
		if int(id.Type) == SubscriptionIDTypeSIPURI {
			s := strings.TrimPrefix(string(id.Data), "sip:")
			s = strings.TrimPrefix(s, "tel:")
			if at := strings.IndexByte(s, '@'); at > 0 {
				s = s[:at]
			}
			return clean(s)
		}
	}
	if len(ids) > 0 {
		return clean(string(ids[0].Data))
	}
	return ""
}

// rxChargingRuleName names the Gx rule for one media component of one Rx
// session. The P-GW keys rules by name within a Gx session, so the name must
// be unique per Rx session: every P-CSCF Session-Id shares the same prefix
// (e.g. "pcscf.ims.mnc099…"), and a prefix-based name made each new Rx
// session modify the previous session's bearer instead of creating its own.
// A re-AAR on the same Rx session keeps the same name and updates its rule.
func rxChargingRuleName(sessionID string, mediaComponentNumber uint32) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return fmt.Sprintf("rx-%08x-%d", h.Sum32(), mediaComponentNumber)
}
