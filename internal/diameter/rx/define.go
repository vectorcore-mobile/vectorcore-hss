package rx

import "github.com/fiorix/go-diameter/v4/diam/datatype"

const (
	Vendor3GPP = uint32(10415)

	// Media-Type values (3GPP TS 29.214 §5.3.19)
	MediaTypeAudio   = 0
	MediaTypeVideo   = 1
	MediaTypeData    = 2
	MediaTypeControl = 4
	MediaTypeOther   = 0xFFFFFFFF

	// Flow-Usage values (3GPP TS 29.214 §5.3.12)
	FlowUsageAFSignalling = 2

	// Subscription-Id-Type values
	SubscriptionIDTypeIMSI   = 1
	SubscriptionIDTypeMSISDN = 0
	SubscriptionIDTypeSIPURI = 2

	// Rx AVP codes (3GPP TS 29.214)
	avpAFApplicationIdentifier  = uint32(504)
	avpFlowStatus               = uint32(511)
	avpSpecificAction           = uint32(513)
	avpMaxRequestedBWDL         = uint32(515)
	avpMaxRequestedBWUL         = uint32(516)
	avpMediaComponentDescription = uint32(517)
	avpMediaComponentNumber     = uint32(518)
	avpMediaSubComponent        = uint32(519)
	avpMediaType                = uint32(520)
	avpFlowDescription          = uint32(507)
	avpFlowNumber               = uint32(509)
	avpRRBandwidth              = uint32(521)
	avpRSBandwidth              = uint32(522)
	avpMinRequestedBWDL         = uint32(534)
	avpMinRequestedBWUL         = uint32(535)

	// Gx AVP codes reused for RAR construction (already registered by gx.LoadDict)
	avpGxChargingRuleInstall     = uint32(1001)
	avpGxChargingRuleRemove      = uint32(1002)
	avpGxChargingRuleDefinition  = uint32(1003)
	avpGxChargingRuleName        = uint32(1005)
	avpGxFlowInformation         = uint32(1058)
	avpGxFlowStatus              = uint32(511)
	avpGxQoSInformation          = uint32(1016)
	avpGxGuaranteedBitrateDL     = uint32(1025)
	avpGxGuaranteedBitrateUL     = uint32(1026)
	avpGxQoSClassIdentifier      = uint32(1028)
	avpGxAllocationRetentionPri  = uint32(1034)
	avpGxPrecedence              = uint32(1010)
	avpGxPriorityLevel           = uint32(1046)
	avpGxPreemptionCapability    = uint32(1047)
	avpGxPreemptionVulnerability = uint32(1048)
	avpGxFlowDirection           = uint32(1080)

	// Pre-Emption-Capability/Vulnerability values (3GPP TS 29.212 §5.3.47/5.3.48)
	// 0 = ENABLED, 1 = DISABLED
	preemptionCapabilityDisabled  = uint32(1)
	preemptionVulnerabilityEnabled = uint32(0)

	// Flow-Direction values (3GPP TS 29.212 §5.3.65)
	flowDirectionDownlink     = uint32(1)
	flowDirectionUplink       = uint32(2)
	flowDirectionBidirectional = uint32(3)

	// Re-Auth-Request-Type values (RFC 6733)
	reAuthRequestTypeAuthorizeOnly = uint32(0)

	// Gx application ID used when sending RAR to PGW
	appIDGx = uint32(16777238)

	// RAR command code (RFC 6733 §8.3.6)
	cmdRAR = uint32(258)
)

// AAR is the parsed AA-Request from the P-CSCF.
type AAR struct {
	SessionID       datatype.UTF8String       `avp:"Session-Id"`
	OriginHost      datatype.DiameterIdentity `avp:"Origin-Host"`
	OriginRealm     datatype.DiameterIdentity `avp:"Origin-Realm"`
	DestinationRealm datatype.DiameterIdentity `avp:"Destination-Realm"`
	SubscriptionIDs []SubscriptionID          `avp:"Subscription-Id"`
	FramedIPAddress datatype.OctetString      `avp:"Framed-IP-Address"`
	CalledStationID datatype.UTF8String       `avp:"Called-Station-Id"`
	MediaComponents []MediaComponentDescription `avp:"Media-Component-Description"`
	RxRequestType   datatype.Enumerated       `avp:"Rx-Request-Type"`
}

// STR is the parsed Session-Termination-Request from the P-CSCF.
type STR struct {
	SessionID        datatype.UTF8String       `avp:"Session-Id"`
	OriginHost       datatype.DiameterIdentity `avp:"Origin-Host"`
	OriginRealm      datatype.DiameterIdentity `avp:"Origin-Realm"`
	DestinationRealm datatype.DiameterIdentity `avp:"Destination-Realm"`
	TerminationCause datatype.Enumerated       `avp:"Termination-Cause"`
}

// MediaSubComponent carries per-flow IP filter rules derived from SDP.
type MediaSubComponent struct {
	FlowNumber       datatype.Unsigned32      `avp:"Flow-Number"`
	FlowDescriptions []datatype.IPFilterRule  `avp:"Flow-Description"`
	FlowUsage        *datatype.Enumerated     `avp:"Flow-Usage"`
}

// MediaComponentDescription carries SDP-derived media stream info from the P-CSCF.
type MediaComponentDescription struct {
	MediaComponentNumber datatype.Unsigned32    `avp:"Media-Component-Number"`
	MediaSubComponents   []MediaSubComponent    `avp:"Media-Sub-Component"`
	MediaType            datatype.Enumerated    `avp:"Media-Type"`
	MaxRequestedBWDL     datatype.Unsigned32    `avp:"Max-Requested-Bandwidth-DL"`
	MaxRequestedBWUL     datatype.Unsigned32    `avp:"Max-Requested-Bandwidth-UL"`
	MinRequestedBWDL     datatype.Unsigned32    `avp:"Min-Requested-Bandwidth-DL"`
	MinRequestedBWUL     datatype.Unsigned32    `avp:"Min-Requested-Bandwidth-UL"`
	RRBandwidth          datatype.Unsigned32    `avp:"RR-Bandwidth"`
	RSBandwidth          datatype.Unsigned32    `avp:"RS-Bandwidth"`
}

type SubscriptionID struct {
	Type datatype.Enumerated `avp:"Subscription-Id-Type"`
	Data datatype.UTF8String `avp:"Subscription-Id-Data"`
}

// rxSession records the state of an active Rx session so we can remove
// dedicated bearer rules when the session is torn down (STR).
type rxSession struct {
	imsi        string
	gxSessionID string // PCRFSessionID from serving_apn → used in Gx RAR
	pgwPeer     string // ServingPGWPeer → connTracker key
	pgwRealm    string // ServingPGWRealm
	ruleNames   []string
}
