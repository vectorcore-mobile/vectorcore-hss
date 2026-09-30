package models

import "time"

// ── APN ─────────────────────────────────────────────────────────────────────

// Preferred-Data-Mode bit positions per 3GPP TS 29.272 §7.3.209 (AVP 1686,
// Unsigned32 bitmask applying to APN.NIDDPreferredDataMode). At least one bit
// must be set when the AVP is emitted; both may be set simultaneously.
const (
	PreferredDataModeUserPlane    = 1 << 0
	PreferredDataModeControlPlane = 1 << 1
	PreferredDataModeValidMask    = PreferredDataModeUserPlane | PreferredDataModeControlPlane
)

// Non-IP-Data-Delivery-Mechanism values per 3GPP TS 29.272 §7.3.205 (AVP 1682).
const (
	NIDDMechanismSGiBased  = 0
	NIDDMechanismSCEFBased = 1
)

// RDS-Indicator values per 3GPP TS 29.272 §7.3.222 (AVP 1697).
const (
	RDSDisabled = 0
	RDSEnabled  = 1
)

type APN struct {
	APNID                      int     `gorm:"column:apn_id;primaryKey;autoIncrement" json:"apn_id,omitempty"`
	APN                        string  `gorm:"column:apn;size:50;not null"            json:"apn"`
	IPVersion                  int     `gorm:"column:ip_version;default:0"            json:"ip_version"`
	PGWAddress                 *string `gorm:"column:pgw_address;size:50"             json:"pgw_address,omitempty"`
	SGWAddress                 *string `gorm:"column:sgw_address;size:50"             json:"sgw_address,omitempty"`
	ChargingCharacteristics    string  `gorm:"column:charging_characteristics;size:4;default:0800" json:"charging_characteristics,omitempty"`
	APNAMBRDown                int     `gorm:"column:apn_ambr_dl;not null"            json:"apn_ambr_dl"`
	APNAMBRUp                  int     `gorm:"column:apn_ambr_ul;not null"            json:"apn_ambr_ul"`
	QCI                        int     `gorm:"column:qci;default:9"                   json:"qci,omitempty"`
	ARPPriority                int     `gorm:"column:arp_priority;default:4"          json:"arp_priority,omitempty"`
	ARPPreemptionCapability    *bool   `gorm:"column:arp_preemption_capability;default:false"  json:"arp_preemption_capability,omitempty"`
	ARPPreemptionVulnerability *bool   `gorm:"column:arp_preemption_vulnerability;default:false" json:"arp_preemption_vulnerability,omitempty"`
	ChargingRuleList           *string `gorm:"column:charging_rule_list;size:18"      json:"charging_rule_list,omitempty"`
	// CIoTEnabled gates APN-level CIoT/NIDD subscription features (Non-IP PDN,
	// SCEF delivery, Preferred Data Mode, RDS). It does NOT authorize a
	// subscriber to use the NB-IoT RAT — that is controlled independently by
	// the subscriber's Access-Restriction-Data bit 6 (models.ARDNBIoTNotAllowed).
	// A UE may use NB-IoT to reach a perfectly ordinary IP APN with this unset,
	// and conversely this may be set on an APN reached over any RAT. Column
	// stays "nbiot" for backward compatibility with existing data.
	CIoTEnabled *bool `gorm:"column:nbiot;default:false" json:"ciot_enabled,omitempty"`
	// NonIPPDN is Non-IP-PDN-Type-Indicator (TS 29.272 §7.3.204). Only
	// meaningful when CIoTEnabled is set; when true, PDNType/IPVersion is
	// ignored by the MME and Non-IP PDN handling applies instead.
	NonIPPDN *bool `gorm:"column:non_ip_pdn;default:false" json:"non_ip_pdn,omitempty"`
	// NIDDScefID/NIDDScefRealm are SCEF-ID (TS 29.336) / SCEF-Realm (§7.3.207).
	// Only emitted when NonIPPDN is true and NIDDMechanism is SCEF-based.
	NIDDScefID    *string `gorm:"column:nidd_scef_id;size:512"    json:"nidd_scef_id,omitempty"`
	NIDDScefRealm *string `gorm:"column:nidd_scef_realm;size:512" json:"nidd_scef_realm,omitempty"`
	// NIDDMechanism is Non-IP-Data-Delivery-Mechanism (§7.3.205): see
	// NIDDMechanismSGiBased/NIDDMechanismSCEFBased. Only meaningful when
	// NonIPPDN is true.
	NIDDMechanism *int `gorm:"column:nidd_mechanism" json:"nidd_mechanism,omitempty"`
	// NIDDRDS is RDS-Indicator (§7.3.222): see RDSDisabled/RDSEnabled. Only
	// meaningful when NonIPPDN is true and NIDDMechanism is SCEF-based.
	NIDDRDS *int `gorm:"column:nidd_rds" json:"nidd_rds,omitempty"`
	// NIDDPreferredDataMode is Preferred-Data-Mode (§7.3.209): a 2-bit mask,
	// see PreferredDataModeUserPlane/PreferredDataModeControlPlane. Applies to
	// IP and Non-IP APNs alike (not gated by NonIPPDN), only by CIoTEnabled.
	NIDDPreferredDataMode *int   `gorm:"column:nidd_preferred_data_mode" json:"nidd_preferred_data_mode,omitempty"`
	LastModified          string `gorm:"column:last_modified;size:100"   json:"last_modified,omitempty"`
}

func (APN) TableName() string { return "apn" }

// ── ALGORITHM_PROFILE ────────────────────────────────────────────────────────

// AlgorithmProfile stores custom Milenage algorithm constants (c1-c5, r1-r5)
// for SIM cards that require non-standard authentication parameters.
// When an AUC references a profile the HSS uses these constants instead of the
// standard 3GPP Milenage defaults.  Deleting a profile causes the AUC to fall
// back to standard Milenage automatically.
type AlgorithmProfile struct {
	AlgorithmProfileID int    `gorm:"column:algorithm_profile_id;primaryKey;autoIncrement" json:"algorithm_profile_id,omitempty"`
	ProfileName        string `gorm:"column:profile_name;size:128;uniqueIndex;not null"    json:"profile_name"`
	// Milenage c constants: 128-bit values encoded as 32 hex characters.
	// Standard values: c1=all-zeros, c2=...01, c3=...02, c4=...04, c5=...08
	C1 string `gorm:"column:c1;size:32;not null" json:"c1"`
	C2 string `gorm:"column:c2;size:32;not null" json:"c2"`
	C3 string `gorm:"column:c3;size:32;not null" json:"c3"`
	C4 string `gorm:"column:c4;size:32;not null" json:"c4"`
	C5 string `gorm:"column:c5;size:32;not null" json:"c5"`
	// Milenage r rotation values in bits.  Must be multiples of 8 (byte-aligned).
	// Standard values: r1=64, r2=0, r3=32, r4=64, r5=96
	R1           int    `gorm:"column:r1;not null;default:64" json:"r1"`
	R2           int    `gorm:"column:r2;not null;default:0"  json:"r2"`
	R3           int    `gorm:"column:r3;not null;default:32" json:"r3"`
	R4           int    `gorm:"column:r4;not null;default:64" json:"r4"`
	R5           int    `gorm:"column:r5;not null;default:96" json:"r5"`
	LastModified string `gorm:"column:last_modified;size:100" json:"last_modified,omitempty"`
}

func (AlgorithmProfile) TableName() string { return "algorithm_profile" }

// ── AUC ─────────────────────────────────────────────────────────────────────

type AUC struct {
	AUCID int     `gorm:"column:auc_id;primaryKey;autoIncrement" json:"auc_id,omitempty"`
	Ki    string  `gorm:"column:ki;size:32;not null"             json:"ki"`
	OPc   string  `gorm:"column:opc;size:32;not null"            json:"opc"`
	AMF   string  `gorm:"column:amf;size:4;not null"             json:"amf"`
	SQN   int64   `gorm:"column:sqn"                             json:"sqn,omitempty"`
	ICCID *string `gorm:"column:iccid;size:20;uniqueIndex"        json:"iccid,omitempty"`
	IMSI  *string `gorm:"column:imsi;size:18;uniqueIndex"         json:"imsi,omitempty"`
	// AlgorithmProfileID links to a custom Milenage profile.  When nil the
	// standard 3GPP Milenage constants are used.
	AlgorithmProfileID *int64  `gorm:"column:algorithm_profile_id" json:"algorithm_profile_id,omitempty"`
	BatchName          *string `gorm:"column:batch_name;size:20"              json:"batch_name,omitempty"`
	SIMVendor          *string `gorm:"column:sim_vendor;size:20"              json:"sim_vendor,omitempty"`
	ESim               *bool   `gorm:"column:esim;default:false"              json:"esim,omitempty"`
	LPA                *string `gorm:"column:lpa;size:128"                    json:"lpa,omitempty"`
	PIN1               *string `gorm:"column:pin1;size:20"                    json:"pin1,omitempty"`
	PIN2               *string `gorm:"column:pin2;size:20"                    json:"pin2,omitempty"`
	PUK1               *string `gorm:"column:puk1;size:20"                    json:"puk1,omitempty"`
	PUK2               *string `gorm:"column:puk2;size:20"                    json:"puk2,omitempty"`
	KID                *string `gorm:"column:kid;size:20"                     json:"kid,omitempty"`
	PSK                *string `gorm:"column:psk;size:128"                    json:"psk,omitempty"`
	DES                *string `gorm:"column:des;size:128"                    json:"des,omitempty"`
	ADM1               *string `gorm:"column:adm1;size:20"                    json:"adm1,omitempty"`
	Algo               string  `gorm:"column:algo;size:20;default:3"          json:"algo,omitempty"`
	Misc1              *string `gorm:"column:misc1;size:128"                  json:"misc1,omitempty"`
	Misc2              *string `gorm:"column:misc2;size:128"                  json:"misc2,omitempty"`
	Misc3              *string `gorm:"column:misc3;size:128"                  json:"misc3,omitempty"`
	Misc4              *string `gorm:"column:misc4;size:128"                  json:"misc4,omitempty"`
	LastModified       string  `gorm:"column:last_modified;size:100"          json:"last_modified,omitempty"`
}

func (AUC) TableName() string { return "auc" }

// ── SUBSCRIBER ───────────────────────────────────────────────────────────────

// Subscriber-Status values per 3GPP TS 29.272 §7.3.29 (AVP 1424, Enumerated).
const (
	SubscriberStatusServiceGranted            = 0
	SubscriberStatusOperatorDeterminedBarring = 1
)

// Operator-Determined-Barring bit positions per 3GPP TS 29.272 §7.3.30
// (AVP 1425, Unsigned32 bitmask). Multiple bits may be set simultaneously.
const (
	ODBAllPacketOrientedServicesBarred                      uint32 = 1 << 0
	ODBRoamerAccessHPLMNAPBarred                            uint32 = 1 << 1
	ODBRoamerAccessVPLMNAPBarred                            uint32 = 1 << 2
	ODBBarringOfAllOutgoingCalls                            uint32 = 1 << 3
	ODBBarringOfAllOutgoingInternationalCalls               uint32 = 1 << 4
	ODBBarringOfAllOutgoingInternationalCallsExceptHomePLMN uint32 = 1 << 5
	ODBBarringOfAllOutgoingInterZonalCalls                  uint32 = 1 << 6
	ODBBarringOfAllOutgoingInterZonalCallsExceptHomePLMN    uint32 = 1 << 7
	ODBBarringOfInternationalExceptHomePLMNAndInterZonal    uint32 = 1 << 8

	// ODBValidMask is the union of all currently-defined TS 29.272 §7.3.30 bits.
	// Reserved/undefined bits must never be accepted or emitted.
	ODBValidMask = ODBAllPacketOrientedServicesBarred |
		ODBRoamerAccessHPLMNAPBarred |
		ODBRoamerAccessVPLMNAPBarred |
		ODBBarringOfAllOutgoingCalls |
		ODBBarringOfAllOutgoingInternationalCalls |
		ODBBarringOfAllOutgoingInternationalCallsExceptHomePLMN |
		ODBBarringOfAllOutgoingInterZonalCalls |
		ODBBarringOfAllOutgoingInterZonalCallsExceptHomePLMN |
		ODBBarringOfInternationalExceptHomePLMNAndInterZonal
)

// Access-Restriction-Data bit positions per 3GPP TS 29.272 §7.3.31 (AVP 1426,
// Unsigned32 bitmask). A set bit means the corresponding access is NOT
// allowed. This is the subscriber's RAT/access authorization mask, distinct
// from APN-level CIoT/NIDD configuration (see the APN struct's CIoTEnabled/
// NonIPPDN fields) — an admin restricting NB-IoT here does not change what an
// APN offers, and enabling CIoT features on an APN does not grant RAT access.
//
// Per §7.3.31 NOTE 2, bits 11 (LTE-M) and 12 (WB-E-UTRAN Except LTE-M) are
// only meaningful when bit 4 (WB-E-UTRAN) is not set; validateAccessRestrictionData
// in internal/api/subscriber.go rejects the contradictory combination rather
// than silently reinterpreting it.
const (
	ARDUTRANNotAllowed                          uint32 = 1 << 0
	ARDGERANNotAllowed                          uint32 = 1 << 1
	ARDGANNotAllowed                            uint32 = 1 << 2
	ARDIHSPAEvolutionNotAllowed                 uint32 = 1 << 3
	ARDWBEUTRANNotAllowed                       uint32 = 1 << 4
	ARDHOToNon3GPPAccessNotAllowed              uint32 = 1 << 5
	ARDNBIoTNotAllowed                          uint32 = 1 << 6
	ARDEnhancedCoverageNotAllowed               uint32 = 1 << 7
	ARDNRAsSecondaryRATNotAllowed               uint32 = 1 << 8
	ARDUnlicensedSpectrumSecondaryRATNotAllowed uint32 = 1 << 9
	ARDNRIn5GSNotAllowed                        uint32 = 1 << 10
	ARDLTEMNotAllowed                           uint32 = 1 << 11
	ARDWBEUTRANExceptLTEMNotAllowed             uint32 = 1 << 12
)

type Subscriber struct {
	SubscriberID          int     `gorm:"column:subscriber_id;primaryKey;autoIncrement"  json:"subscriber_id,omitempty"`
	IMSI                  string  `gorm:"column:imsi;size:18;uniqueIndex;not null"        json:"imsi"`
	Enabled               *bool   `gorm:"column:enabled;default:true"                     json:"enabled,omitempty"`
	AUCID                 int     `gorm:"column:auc_id;not null"                          json:"auc_id"`
	DefaultAPN            int     `gorm:"column:default_apn;not null"                     json:"default_apn"`
	APNList               string  `gorm:"column:apn_list;size:64;not null"                json:"apn_list"`
	MSISDN                *string `gorm:"column:msisdn;size:18"                           json:"msisdn,omitempty"`
	Alias                 *string `gorm:"column:alias;size:32"                            json:"alias,omitempty"`
	UEAMBRDown            int     `gorm:"column:ue_ambr_dl;default:999999"                json:"ue_ambr_dl,omitempty"`
	UEAMBRUp              int     `gorm:"column:ue_ambr_ul;default:999999"                json:"ue_ambr_ul,omitempty"`
	NAM                   int     `gorm:"column:nam;default:0"                            json:"nam,omitempty"`
	AccessRestrictionData *uint32 `gorm:"column:access_restriction_data"                  json:"access_restriction_data,omitempty"`
	RoamingEnabled        *bool   `gorm:"column:roaming_enabled;default:true"             json:"roaming_enabled,omitempty"`
	// SubscriberStatus is the Subscriber-Status AVP (TS 29.272 §7.3.29):
	// 0=SERVICE_GRANTED, 1=OPERATOR_DETERMINED_BARRING. Independent of Enabled
	// and RoamingEnabled, which remain the HSS's own admin/roaming controls.
	SubscriberStatus int `gorm:"column:subscriber_status;default:0" json:"subscriber_status,omitempty"`
	// OperatorDeterminedBarring is the Operator-Determined-Barring AVP bitmask
	// (TS 29.272 §7.3.30). Only meaningful when SubscriberStatus is
	// OPERATOR_DETERMINED_BARRING; must be 0 otherwise. Separate from
	// AccessRestrictionData, which encodes unrelated Access-Restriction-Data semantics.
	OperatorDeterminedBarring   uint32     `gorm:"column:operator_determined_barring;default:0"    json:"operator_determined_barring,omitempty"`
	SubscribedRAUTAUTimer       int        `gorm:"column:subscribed_rau_tau_timer;default:300"     json:"subscribed_rau_tau_timer,omitempty"`
	ServingMME                  *string    `gorm:"column:serving_mme;size:512"                     json:"serving_mme,omitempty"`
	ServingMMETimestamp         *time.Time `gorm:"column:serving_mme_timestamp"                    json:"serving_mme_timestamp,omitempty"`
	ServingMMERealm             *string    `gorm:"column:serving_mme_realm;size:512"               json:"serving_mme_realm,omitempty"`
	ServingMMEPeer              *string    `gorm:"column:serving_mme_peer;size:512"                json:"serving_mme_peer,omitempty"`
	MMENumberForMTSMS           *string    `gorm:"column:mme_number_for_mt_sms;size:32"            json:"mme_number_for_mt_sms,omitempty"`
	MMERegisteredForSMS         *bool      `gorm:"column:mme_registered_for_sms"                   json:"mme_registered_for_sms,omitempty"`
	SMSRegisterRequest          *int       `gorm:"column:sms_register_request"                     json:"sms_register_request,omitempty"`
	SMSRegistrationTimestamp    *time.Time `gorm:"column:sms_registration_timestamp"               json:"sms_registration_timestamp,omitempty"`
	ServingMSC                  *string    `gorm:"column:serving_msc;size:512"                     json:"serving_msc,omitempty"`
	ServingMSCTimestamp         *time.Time `gorm:"column:serving_msc_timestamp"                    json:"serving_msc_timestamp,omitempty"`
	ServingVLR                  *string    `gorm:"column:serving_vlr;size:512"                     json:"serving_vlr,omitempty"`
	ServingVLRTimestamp         *time.Time `gorm:"column:serving_vlr_timestamp"                    json:"serving_vlr_timestamp,omitempty"`
	ServingSGSN                 *string    `gorm:"column:serving_sgsn;size:512"                    json:"serving_sgsn,omitempty"`
	ServingSGSNTimestamp        *time.Time `gorm:"column:serving_sgsn_timestamp"                   json:"serving_sgsn_timestamp,omitempty"`
	LastSeenECI                 *string    `gorm:"column:last_seen_eci;size:64"                    json:"last_seen_eci,omitempty"`
	LastSeenENodeBID            *string    `gorm:"column:last_seen_enodeb_id;size:64"              json:"last_seen_enodeb_id,omitempty"`
	LastSeenCellID              *string    `gorm:"column:last_seen_cell_id;size:64"                json:"last_seen_cell_id,omitempty"`
	LastSeenTAC                 *string    `gorm:"column:last_seen_tac;size:64"                    json:"last_seen_tac,omitempty"`
	LastSeenMCC                 *string    `gorm:"column:last_seen_mcc;size:3"                     json:"last_seen_mcc,omitempty"`
	LastSeenMNC                 *string    `gorm:"column:last_seen_mnc;size:3"                     json:"last_seen_mnc,omitempty"`
	LastLocationUpdateTimestamp *time.Time `gorm:"column:last_location_update_timestamp"           json:"last_location_update_timestamp,omitempty"`
	LastModified                string     `gorm:"column:last_modified;size:100"                   json:"last_modified,omitempty"`

	// 5G / UDM fields
	// NSSAI is a JSON array of allowed S-NSSAIs, e.g. [{"sst":1},{"sst":2,"sd":"000001"}].
	// Defaults to [{"sst":1}] (eMBB slice) when empty or NULL.
	NSSAI                *string    `gorm:"column:nssai;type:text"                          json:"nssai,omitempty"`
	ServingAMF           *string    `gorm:"column:serving_amf;size:512"                     json:"serving_amf,omitempty"`
	ServingAMFTimestamp  *time.Time `gorm:"column:serving_amf_timestamp"                    json:"serving_amf_timestamp,omitempty"`
	ServingAMFInstanceID *string    `gorm:"column:serving_amf_instance_id;size:64"          json:"serving_amf_instance_id,omitempty"`
}

func (Subscriber) TableName() string { return "subscriber" }

// ── SERVING_PDU_SESSION (5G SMF registrations via Nudm_UECM) ─────────────────

// ServingPDUSession tracks an active 5G PDU session registered by the SMF.
type ServingPDUSession struct {
	ID            int       `gorm:"column:id;primaryKey;autoIncrement"  json:"id,omitempty"`
	IMSI          string    `gorm:"column:imsi;size:18;index;not null"  json:"imsi"`
	PDUSessionID  int       `gorm:"column:pdu_session_id;not null"      json:"pdu_session_id"`
	SMFInstanceID string    `gorm:"column:smf_instance_id;size:64"      json:"smf_instance_id,omitempty"`
	SMFAddress    string    `gorm:"column:smf_address;size:512"         json:"smf_address,omitempty"`
	SMFSetID      string    `gorm:"column:smf_set_id;size:64"           json:"smf_set_id,omitempty"`
	DNN           string    `gorm:"column:dnn;size:100"                 json:"dnn,omitempty"`
	SNSSAI        string    `gorm:"column:snssai;type:text"             json:"snssai,omitempty"`
	PLMNIDStr     string    `gorm:"column:plmn_id;size:16"              json:"plmn_id,omitempty"`
	Timestamp     time.Time `gorm:"column:timestamp;autoCreateTime"     json:"timestamp,omitempty"`
}

func (ServingPDUSession) TableName() string { return "serving_pdu_session" }

// ── SUBSCRIBER_ROUTING ────────────────────────────────────────────────────────

type SubscriberRouting struct {
	SubscriberRoutingID int     `gorm:"column:subscriber_routing_id;primaryKey;autoIncrement"  json:"subscriber_routing_id,omitempty"`
	SubscriberID        int     `gorm:"column:subscriber_id;not null;uniqueIndex:uidx_sub_apn" json:"subscriber_id"`
	APNID               int     `gorm:"column:apn_id;not null;uniqueIndex:uidx_sub_apn"        json:"apn_id"`
	IPVersion           int     `gorm:"column:ip_version;default:0"                            json:"ip_version"`
	IPAddress           *string `gorm:"column:ip_address;size:254"                             json:"ip_address,omitempty"`
	LastModified        string  `gorm:"column:last_modified;size:100"                          json:"last_modified,omitempty"`
}

func (SubscriberRouting) TableName() string { return "subscriber_routing" }

// ── SERVING_APN ───────────────────────────────────────────────────────────────

type ServingAPN struct {
	ServingAPNID      int     `gorm:"column:serving_apn_id;primaryKey;autoIncrement" json:"serving_apn_id,omitempty"`
	SubscriberID      int     `gorm:"column:subscriber_id;not null;uniqueIndex:uidx_serving_sub_apn" json:"subscriber_id"`
	APNID             int     `gorm:"column:apn;uniqueIndex:uidx_serving_sub_apn;default:0"          json:"apn"`
	APNName           string  `gorm:"column:apn_name;size:100"                       json:"apn_name,omitempty"`
	PCRFSessionID     *string `gorm:"column:pcrf_session_id;size:100"                json:"pcrf_session_id,omitempty"`
	SubscriberRouting *string `gorm:"column:subscriber_routing;size:100"             json:"subscriber_routing,omitempty"`
	IPVersion         int     `gorm:"column:ip_version;default:0"                    json:"ip_version"`
	UEIP              *string `gorm:"column:ue_ip;size:64"                           json:"ue_ip,omitempty"`
	// UEIPv6Prefix is the PDN's IPv6 /64 ("2001:db8:1:2::/64"), the Rx
	// binding key for IPv6 (TS 29.213 §4); ue_ip holds only the IPv4 address.
	UEIPv6Prefix        *string    `gorm:"column:ue_ipv6_prefix;size:64;index" json:"ue_ipv6_prefix,omitempty"`
	ServingPGW          *string    `gorm:"column:serving_pgw;size:512"                    json:"serving_pgw,omitempty"`
	ServingPGWTimestamp *time.Time `gorm:"column:serving_pgw_timestamp"                   json:"serving_pgw_timestamp,omitempty"`
	ServingPGWRealm     *string    `gorm:"column:serving_pgw_realm;size:512"              json:"serving_pgw_realm,omitempty"`
	ServingPGWPeer      *string    `gorm:"column:serving_pgw_peer;size:512"               json:"serving_pgw_peer,omitempty"`
	LastModified        string     `gorm:"column:last_modified;size:100"                  json:"last_modified,omitempty"`
}

func (ServingAPN) TableName() string { return "serving_apn" }

// ── IMS_SUBSCRIBER ────────────────────────────────────────────────────────────

type IMSSubscriber struct {
	IMSSubscriberID    int        `gorm:"column:ims_subscriber_id;primaryKey;autoIncrement" json:"ims_subscriber_id,omitempty"`
	MSISDN             string     `gorm:"column:msisdn;size:18;uniqueIndex"                  json:"msisdn"`
	IMSI               *string    `gorm:"column:imsi;size:18"                                json:"imsi,omitempty"`
	IFCProfileID       *int       `gorm:"column:ifc_profile_id"                              json:"ifc_profile_id,omitempty"`
	PCSCF              *string    `gorm:"column:pcscf;size:512"                              json:"pcscf,omitempty"`
	PCSCFRealm         *string    `gorm:"column:pcscf_realm;size:512"                        json:"pcscf_realm,omitempty"`
	PCSCFActiveSession *string    `gorm:"column:pcscf_active_session;size:512"               json:"pcscf_active_session,omitempty"`
	PCSCFTimestamp     *time.Time `gorm:"column:pcscf_timestamp"                             json:"pcscf_timestamp,omitempty"`
	PCSCFPeer          *string    `gorm:"column:pcscf_peer;size:512"                         json:"pcscf_peer,omitempty"`
	XCAPProfile        *string    `gorm:"column:xcap_profile;type:text"                      json:"xcap_profile,omitempty"`
	ShProfile          *string    `gorm:"column:sh_profile;type:text"                        json:"sh_profile,omitempty"`
	SCSCF              *string    `gorm:"column:scscf;size:512"                              json:"scscf,omitempty"`
	SCSCFTimestamp     *time.Time `gorm:"column:scscf_timestamp"                             json:"scscf_timestamp,omitempty"`
	SCSCFRealm         *string    `gorm:"column:scscf_realm;size:512"                        json:"scscf_realm,omitempty"`
	SCSCFPeer          *string    `gorm:"column:scscf_peer;size:512"                         json:"scscf_peer,omitempty"`
	SHTemplatePath     *string    `gorm:"column:sh_template_path;size:512"                   json:"sh_template_path,omitempty"`
	LastModified       string     `gorm:"column:last_modified;size:100"                      json:"last_modified,omitempty"`
}

func (IMSSubscriber) TableName() string { return "ims_subscriber" }

// ── IFC_PROFILE ───────────────────────────────────────────────────────────────

type IFCProfile struct {
	IFCProfileID int    `gorm:"column:ifc_profile_id;primaryKey;autoIncrement" json:"ifc_profile_id,omitempty"`
	Name         string `gorm:"column:name;size:128;not null"                  json:"name"`
	XMLData      string `gorm:"column:xml_data;type:text;not null"             json:"xml_data"`
	LastModified string `gorm:"column:last_modified;size:100"                  json:"last_modified,omitempty"`
}

func (IFCProfile) TableName() string { return "ifc_profile" }

// ── ROAMING_NETWORK ───────────────────────────────────────────────────────────

type RoamingRules struct {
	RoamingRuleID int     `gorm:"column:roaming_rule_id;primaryKey;autoIncrement" json:"roaming_rule_id,omitempty"`
	Name          *string `gorm:"column:name;size:512"                            json:"name,omitempty"`
	MCC           *string `gorm:"column:mcc;size:10"                              json:"mcc,omitempty"`
	MNC           *string `gorm:"column:mnc;size:10"                              json:"mnc,omitempty"`
	Allow         *bool   `gorm:"column:allow;default:true"                       json:"allow,omitempty"`
	Enabled       *bool   `gorm:"column:enabled;default:true"                     json:"enabled,omitempty"`
	LastModified  string  `gorm:"column:last_modified;size:100"                   json:"last_modified,omitempty"`
}

func (RoamingRules) TableName() string { return "roaming_rules" }

// ── EMERGENCY_SUBSCRIBER ──────────────────────────────────────────────────────

type EmergencySubscriber struct {
	EmergencySubscriberID        int     `gorm:"column:emergency_subscriber_id;primaryKey;autoIncrement" json:"emergency_subscriber_id,omitempty"`
	IMSI                         *string `gorm:"column:imsi;size:18;uniqueIndex"                          json:"imsi,omitempty"`
	ServingPGW                   *string `gorm:"column:serving_pgw;size:512"                              json:"serving_pgw,omitempty"`
	ServingPGWTimestamp          *string `gorm:"column:serving_pgw_timestamp;size:512"                    json:"serving_pgw_timestamp,omitempty"`
	ServingPCSCF                 *string `gorm:"column:serving_pcscf;size:512"                            json:"serving_pcscf,omitempty"`
	ServingPCSCFTimestamp        *string `gorm:"column:serving_pcscf_timestamp;size:512"                  json:"serving_pcscf_timestamp,omitempty"`
	GxOriginRealm                *string `gorm:"column:gx_origin_realm;size:512"                          json:"gx_origin_realm,omitempty"`
	GxOriginHost                 *string `gorm:"column:gx_origin_host;size:512"                           json:"gx_origin_host,omitempty"`
	RATType                      *string `gorm:"column:rat_type;size:512"                                 json:"rat_type,omitempty"`
	IP                           *string `gorm:"column:ip;size:512"                                       json:"ip,omitempty"`
	AccessNetworkGatewayAddress  *string `gorm:"column:access_network_gateway_address;size:512"           json:"access_network_gateway_address,omitempty"`
	AccessNetworkChargingAddress *string `gorm:"column:access_network_charging_address;size:512"          json:"access_network_charging_address,omitempty"`
	LastModified                 string  `gorm:"column:last_modified;size:100"                            json:"last_modified,omitempty"`
}

func (EmergencySubscriber) TableName() string { return "emergency_subscriber" }

// ── CHARGING_RULE ─────────────────────────────────────────────────────────────

type ChargingRule struct {
	ChargingRuleID             int    `gorm:"column:charging_rule_id;primaryKey;autoIncrement" json:"charging_rule_id,omitempty"`
	RuleName                   string `gorm:"column:rule_name;size:20"                         json:"rule_name"`
	QCI                        int    `gorm:"column:qci;default:9"                             json:"qci,omitempty"`
	ARPPriority                int    `gorm:"column:arp_priority;default:4"                    json:"arp_priority,omitempty"`
	ARPPreemptionCapability    *bool  `gorm:"column:arp_preemption_capability;default:false"   json:"arp_preemption_capability,omitempty"`
	ARPPreemptionVulnerability *bool  `gorm:"column:arp_preemption_vulnerability;default:false" json:"arp_preemption_vulnerability,omitempty"`
	MBRDown                    int    `gorm:"column:mbr_dl;not null"                           json:"mbr_dl"`
	MBRUp                      int    `gorm:"column:mbr_ul;not null"                           json:"mbr_ul"`
	GBRDown                    int    `gorm:"column:gbr_dl;not null"                           json:"gbr_dl"`
	GBRUp                      int    `gorm:"column:gbr_ul;not null"                           json:"gbr_ul"`
	TFTGroupID                 *int   `gorm:"column:tft_group_id"                              json:"tft_group_id,omitempty"`
	Precedence                 *int   `gorm:"column:precedence"                                json:"precedence,omitempty"`
	RatingGroup                *int   `gorm:"column:rating_group"                              json:"rating_group,omitempty"`
	LastModified               string `gorm:"column:last_modified;size:100"                    json:"last_modified,omitempty"`
}

func (ChargingRule) TableName() string { return "charging_rule" }

// ── TFT ───────────────────────────────────────────────────────────────────────

type TFT struct {
	TFTID        int    `gorm:"column:tft_id;primaryKey;autoIncrement" json:"tft_id,omitempty"`
	TFTGroupID   int    `gorm:"column:tft_group_id;not null"           json:"tft_group_id"`
	TFTString    string `gorm:"column:tft_string;size:100;not null"    json:"tft_string"`
	Direction    int    `gorm:"column:direction;not null"              json:"direction"`
	LastModified string `gorm:"column:last_modified;size:100"          json:"last_modified,omitempty"`
}

func (TFT) TableName() string { return "tft" }

// ── EIR ───────────────────────────────────────────────────────────────────────

type EIR struct {
	EIRID             int     `gorm:"column:eir_id;primaryKey;autoIncrement" json:"eir_id,omitempty"`
	IMEI              *string `gorm:"column:imei;size:60"                    json:"imei,omitempty"`
	IMSI              *string `gorm:"column:imsi;size:60"                    json:"imsi,omitempty"`
	RegexMode         int     `gorm:"column:regex_mode;default:1"            json:"regex_mode,omitempty"`
	MatchResponseCode int     `gorm:"column:match_response_code"             json:"match_response_code"`
	LastModified      string  `gorm:"column:last_modified;size:100"          json:"last_modified,omitempty"`
}

func (EIR) TableName() string { return "eir" }

// ── IMSI_IMEI_HISTORY ─────────────────────────────────────────────────────────

type IMSIIMEIHistory struct {
	IMSIIMEIHistoryID int        `gorm:"column:imsi_imei_history_id;primaryKey;autoIncrement"    json:"imsi_imei_history_id,omitempty"`
	IMSI              string     `gorm:"column:imsi;size:20;uniqueIndex:idx_imsi_imei"           json:"imsi"`
	IMEI              string     `gorm:"column:imei;size:20;uniqueIndex:idx_imsi_imei;index"     json:"imei"`
	MatchResponseCode int        `gorm:"column:match_response_code"                              json:"match_response_code"`
	IMSIIMEITimestamp *time.Time `gorm:"column:imsi_imei_timestamp"                              json:"imsi_imei_timestamp,omitempty"`
	// Device info populated from TAC DB at write time so the web UI needs no
	// secondary lookup.  Empty when the TAC is not in the database.
	Make         string `gorm:"column:make;size:128"      json:"make,omitempty"`
	Model        string `gorm:"column:model;size:128"     json:"model,omitempty"`
	LastModified string `gorm:"column:last_modified;size:100" json:"last_modified,omitempty"`
}

func (IMSIIMEIHistory) TableName() string { return "eir_history" }

// ── TAC ───────────────────────────────────────────────────────────────────────

// TACModel stores the GSMA Type Allocation Code database used to identify
// device make and model from the first 8 digits of an IMEI.
// The struct is named TACModel to avoid colliding with the repository TAC field.
type TACModel struct {
	TACID        int    `gorm:"column:tac_id;primaryKey;autoIncrement" json:"tac_id,omitempty"`
	TAC          string `gorm:"column:tac;size:8;uniqueIndex;not null"  json:"tac"`
	Make         string `gorm:"column:make;size:128;not null"           json:"make"`
	Model        string `gorm:"column:model;size:128;not null"          json:"model"`
	LastModified string `gorm:"column:last_modified;size:100"           json:"last_modified,omitempty"`
}

func (TACModel) TableName() string { return "tac" }

// ── SUBSCRIBER_ATTRIBUTES ─────────────────────────────────────────────────────

type SubscriberAttribute struct {
	SubscriberAttributeID int    `gorm:"column:subscriber_attributes_id;primaryKey;autoIncrement" json:"subscriber_attributes_id,omitempty"`
	SubscriberID          int    `gorm:"column:subscriber_id;not null"                             json:"subscriber_id"`
	Key                   string `gorm:"column:key;size:60"                                        json:"key"`
	Value                 string `gorm:"column:value;size:12000"                                   json:"value"`
	LastModified          string `gorm:"column:last_modified;size:100"                             json:"last_modified,omitempty"`
}

func (SubscriberAttribute) TableName() string { return "subscriber_attributes" }

// ── OPERATION_LOG ─────────────────────────────────────────────────────────────

type OperationLog struct {
	ID           int       `gorm:"column:id;primaryKey;autoIncrement"   json:"id,omitempty"`
	ItemID       int       `gorm:"column:item_id;not null"              json:"item_id"`
	OperationID  string    `gorm:"column:operation_id;size:36;not null" json:"operation_id"`
	Operation    string    `gorm:"column:operation;size:10"             json:"operation"`
	Changes      *string   `gorm:"column:changes;type:text"             json:"changes,omitempty"`
	LastModified string    `gorm:"column:last_modified;size:100"        json:"last_modified,omitempty"`
	Timestamp    time.Time `gorm:"column:timestamp;autoCreateTime"      json:"timestamp,omitempty"`
	DBTableName  string    `gorm:"column:table_name;size:255"           json:"table_name"`
}

func (OperationLog) TableName() string { return "operation_log" }

// ── MESSAGE_WAITING_DATA ──────────────────────────────────────────────────────

// MessageWaitingData stores pending SMS notification requests from SMS-SCs.
// Created by RSDS when delivery fails; consumed and deleted by ALR when the
// subscriber registers (ULR success). Part of the S6c interface (TS 29.338).
type MessageWaitingData struct {
	ID                     int        `gorm:"column:id;primaryKey;autoIncrement"                  json:"id,omitempty"`
	IMSI                   string     `gorm:"column:imsi;size:18;index;not null"                  json:"imsi"`
	SCAddress              string     `gorm:"column:sc_address;size:32;not null"                  json:"sc_address"`
	SCOriginHost           string     `gorm:"column:sc_origin_host;size:512;not null"             json:"sc_origin_host"`
	SCOriginRealm          string     `gorm:"column:sc_origin_realm;size:512;not null"            json:"sc_origin_realm"`
	SMRPMTI                int        `gorm:"column:sm_rp_mti;not null;default:0"                json:"sm_rp_mti"`
	MWDStatusFlags         uint32     `gorm:"column:mwd_status_flags;not null;default:0"          json:"mwd_status_flags"`
	SMSMICorrelationID     *string    `gorm:"column:smsmi_correlation_id;type:text"               json:"smsmi_correlation_id,omitempty"`
	AbsentUserDiagnosticSM *uint32    `gorm:"column:absent_user_diagnostic_sm"                    json:"absent_user_diagnostic_sm,omitempty"`
	LastAlertTrigger       *string    `gorm:"column:last_alert_trigger;size:64"                   json:"last_alert_trigger,omitempty"`
	LastAlertAttemptAt     *time.Time `gorm:"column:last_alert_attempt_at"                        json:"last_alert_attempt_at,omitempty"`
	AlertAttemptCount      uint32     `gorm:"column:alert_attempt_count;not null;default:0"      json:"alert_attempt_count"`
	CreatedAt              time.Time  `gorm:"column:created_at;autoCreateTime"                    json:"created_at,omitempty"`
	LastModified           string     `gorm:"column:last_modified;size:100"                       json:"last_modified,omitempty"`
}

func (MessageWaitingData) TableName() string { return "message_waiting_data" }

// AllModels returns every model for db.AutoMigrate().
func AllModels() []interface{} {
	return []interface{}{
		&AlgorithmProfile{}, &APN{}, &AUC{}, &Subscriber{}, &SubscriberRouting{},
		&ServingAPN{}, &IMSSubscriber{}, &RoamingRules{},
		&EmergencySubscriber{}, &ChargingRule{},
		&TFT{}, &EIR{}, &IMSIIMEIHistory{}, &SubscriberAttribute{},
		&OperationLog{}, &IFCProfile{}, &TACModel{},
		&ServingPDUSession{}, &MessageWaitingData{},
	}
}
