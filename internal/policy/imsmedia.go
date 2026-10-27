package policy

// IMS media QoS shared by the Rx (VoLTE) and N5 (VoNR) application
// functions, so a call gets the same dedicated bearer / QoS flow on either
// access. Values are hardcoded for now.

// IMSMediaType is an IMS media component type, independent of how Rx
// (Media-Type enum) or N5 (medType string) encodes it.
type IMSMediaType int

const (
	IMSMediaOther IMSMediaType = iota
	IMSMediaAudio
	IMSMediaVideo
	IMSMediaControl
)

// IMSFlowUsage is a media sub-component's Flow-Usage / flowUsage.
type IMSFlowUsage int

const (
	IMSFlowUsageNoInfo IMSFlowUsage = iota
	IMSFlowUsageRTCP
	IMSFlowUsageAFSignalling
)

// IMSMediaQoS is the QoS a media component's rule is installed with.
//
// ARP values are per 3GPP TS 23.203 §6.1.7 QCI characteristics:
//
//	QCI 1 (voice): priority 2 → use 14 per common operator practice
//	QCI 2 (video): priority 4 → use 11 per common operator practice
//
// Pre-Emption-Capability is always DISABLED and Pre-Emption-Vulnerability is
// always ENABLED for these dedicated IMS bearers (set by the caller).
type IMSMediaQoS struct {
	QCI         int    // QCI / 5QI
	GBR         bool   // true if a GBR bearer is required (QCI 1–4)
	DefaultBW   uint32 // default MBR/GBR in bps, applied when the AF omits bandwidth; also the cap
	ARPPriority uint32 // ARP Priority-Level
	Precedence  uint32 // rule precedence (lower = higher priority)
}

// IMSMediaQoSFor returns the QoS for a media type. ok is false when the
// type gets no dedicated rule. CONTROL (SIP signalling) gets no rule: it
// uses the IMS APN's default bearer and the QoS configured on that APN.
func IMSMediaQoSFor(t IMSMediaType) (q IMSMediaQoS, ok bool) {
	switch t {
	case IMSMediaAudio:
		return IMSMediaQoS{QCI: 1, GBR: true, DefaultBW: 128000, ARPPriority: 14, Precedence: 40}, true // GBR VoLTE voice, 128 kbps
	case IMSMediaVideo:
		return IMSMediaQoS{QCI: 2, GBR: true, DefaultBW: 512000, ARPPriority: 11, Precedence: 30}, true // GBR VoLTE video, 512 kbps
	default:
		return IMSMediaQoS{}, false // no dedicated bearer
	}
}

// IsIMSSignalling reports whether a media component describes AF
// signalling: media type CONTROL, or every sub-component marked
// AF_SIGNALLING (TS 29.214 §5.3.12, TS 29.514), which P-CSCFs send at
// registration with or without a media type. subUsages holds one entry per
// sub-component; an absent Flow-Usage is IMSFlowUsageNoInfo.
func IsIMSSignalling(t IMSMediaType, subUsages []IMSFlowUsage) bool {
	if t == IMSMediaControl {
		return true
	}
	if len(subUsages) == 0 {
		return false
	}
	for _, u := range subUsages {
		if u != IMSFlowUsageAFSignalling {
			return false
		}
	}
	return true
}
