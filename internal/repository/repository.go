package repository

import (
	"context"
	"time"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

var ErrNotFound = errorString("not found")

type errorString string

func (e errorString) Error() string { return string(e) }

type Repository interface {
	// AUC
	GetAUCByIMSI(ctx context.Context, imsi string) (*models.AUC, error)
	GetAUCByID(ctx context.Context, aucID int) (*models.AUC, error)
	AtomicGetAndIncrementSQN(ctx context.Context, aucID int, delta int64) (*models.AUC, error)
	ResyncSQN(ctx context.Context, aucID int, newSQN int64) error
	GetAlgorithmProfile(ctx context.Context, id int64) (*models.AlgorithmProfile, error)

	// APN
	GetAPNByID(ctx context.Context, apnID int) (*models.APN, error)
	// GetAPNsByIDs batches lookup for multiple APN IDs into one query, used
	// by ULR to avoid an N+1 query per subscriber APN. Order is not
	// guaranteed to match ids; missing IDs are simply absent from the result.
	GetAPNsByIDs(ctx context.Context, ids []int) ([]models.APN, error)

	// Subscriber
	GetSubscriberByIMSI(ctx context.Context, imsi string) (*models.Subscriber, error)
	GetSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.Subscriber, error)
	UpdateServingMME(ctx context.Context, imsi string, update *ServingMMEUpdate) error
	UpdateServingSGSN(ctx context.Context, imsi string, update *ServingSGSNUpdate) error
	UpdateServingVLR(ctx context.Context, imsi string, update *ServingVLRUpdate) error
	UpdateServingMSC(ctx context.Context, imsi string, update *ServingMSCUpdate) error
	UpdateServingAMF(ctx context.Context, imsi string, update *ServingAMFUpdate) error

	// 5G PDU session tracking (Nudm_UECM SMF registrations)
	UpsertServingPDUSession(ctx context.Context, rec *models.ServingPDUSession) error
	DeleteServingPDUSession(ctx context.Context, imsi string, pduSessionID int) error
	ListServingPDUSessions(ctx context.Context, imsi string) ([]models.ServingPDUSession, error)

	// IMS (Cx/Sh)
	GetIMSSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.IMSSubscriber, error)
	GetIMSSubscriberByIMSI(ctx context.Context, imsi string) (*models.IMSSubscriber, error)
	UpdateIMSSCSCF(ctx context.Context, msisdn string, update *IMSSCSCFUpdate) error
	UpdateIMSPCSCF(ctx context.Context, msisdn string, update *IMSPCSCFUpdate) error
	GetIFCProfileByID(ctx context.Context, id int) (*models.IFCProfile, error)

	// Gx / Rx
	GetAPNByName(ctx context.Context, apnName string) (*models.APN, error)
	GetAllChargingRules(ctx context.Context) ([]models.ChargingRule, error)
	GetChargingRulesByNames(ctx context.Context, names []string) ([]models.ChargingRule, error)
	GetChargingRulesByIDs(ctx context.Context, ids []int) ([]models.ChargingRule, error)
	GetTFTsByGroupID(ctx context.Context, groupID int) ([]models.TFT, error)
	UpsertServingAPN(ctx context.Context, record *models.ServingAPN) error
	DeleteServingAPNBySession(ctx context.Context, pcrfSessionID string) error
	GetServingAPNBySession(ctx context.Context, pcrfSessionID string) (*models.ServingAPN, error)
	GetServingAPNByIMSI(ctx context.Context, imsi string) (*models.ServingAPN, error)
	GetServingAPNByMSISDN(ctx context.Context, msisdn string) (*models.ServingAPN, error)
	// GetServingAPNByIdentity finds an active Gx session by matching identity
	// against both imsi and msisdn columns in a single query. Used by the Rx
	// AAR handler where the P-CSCF may send any Subscription-ID type.
	GetServingAPNByIdentity(ctx context.Context, identity string) (*models.ServingAPN, error)
	GetServingAPNByUEIP(ctx context.Context, ueIP string) (*models.ServingAPN, error)
	// GetServingAPNByUEIPv6Prefix finds the Gx session whose PDN owns the
	// IPv6 /64 key (e.g. "2001:db8:1:2::/64").
	GetServingAPNByUEIPv6Prefix(ctx context.Context, prefix string) (*models.ServingAPN, error)

	// Subscriber Routing (static IP assignment)
	GetSubscriberRoutingBySubscriberAndAPN(ctx context.Context, subscriberID, apnID int) (*models.SubscriberRouting, error)
	// GetSubscriberRoutingsBySubscriberAndAPNs batches lookup for a
	// subscriber's static-IP routing across multiple APNs into one query,
	// used by ULR to avoid an N+1 query per subscriber APN.
	GetSubscriberRoutingsBySubscriberAndAPNs(ctx context.Context, subscriberID int, apnIDs []int) ([]models.SubscriberRouting, error)

	// Roaming
	GetRoamingRuleByMCCMNC(ctx context.Context, mcc, mnc string) (*models.RoamingRules, error)

	// Emergency subscribers (runtime state, written by Gx CCR)
	UpsertEmergencySubscriber(ctx context.Context, rec *models.EmergencySubscriber) error
	DeleteEmergencySubscriberByIMSI(ctx context.Context, imsi string) error

	// EIR (S13)
	ListEIR(ctx context.Context, out *[]models.EIR) error
	EIRNoMatchResponse() int
	UpsertIMSIIMEIHistory(ctx context.Context, imsi, imei, make, model string, matchResponseCode int) error

	// MWD (S6c — Message Waiting Data)
	StoreMWD(ctx context.Context, rec *models.MessageWaitingData) error
	GetMWDForIMSI(ctx context.Context, imsi string) ([]models.MessageWaitingData, error)
	DeleteMWD(ctx context.Context, imsi, scAddr string) error

	// Cache
	InvalidateCache(imsi string)
	InvalidateAPNCache(apnID int)

	// GeoRed — snapshot reads (full table scans, used only for resync).
	ListAllAUC(ctx context.Context) ([]models.AUC, error)
	ListAllSubscribers(ctx context.Context) ([]models.Subscriber, error)
	ListAllIMSSubscribers(ctx context.Context) ([]models.IMSSubscriber, error)
	ListAllServingAPN(ctx context.Context) ([]GeoredServingAPN, error)

	// GeoRed — OAM record apply (used when receiving OAM events from peers).
	UpsertSubscriber(ctx context.Context, rec *models.Subscriber) error
	DeleteSubscriberByIMSI(ctx context.Context, imsi string) error
	UpsertAUC(ctx context.Context, rec *models.AUC) error
	DeleteAUCByID(ctx context.Context, id int) error
	UpsertAPN(ctx context.Context, rec *models.APN) error
	DeleteAPNByID(ctx context.Context, id int) error
	UpsertIMSSubscriber(ctx context.Context, rec *models.IMSSubscriber) error
	DeleteIMSSubscriberByMSISDN(ctx context.Context, msisdn string) error
	UpsertEIR(ctx context.Context, rec *models.EIR) error
	DeleteEIRByID(ctx context.Context, id int) error
}

// GeoredServingAPN is a serving_apn row joined with subscriber identity.
// Used exclusively by BuildSnapshot for GeoRed state replication.
type GeoredServingAPN struct {
	PCRFSessionID string
	IMSI          string
	MSISDN        *string
	APNID         *int
	APNName       *string
	PGWIP         *string
	UEIP          *string
}

type ServingMMEUpdate struct {
	ServingMME               *string
	Realm                    *string
	Peer                     *string
	Timestamp                *time.Time
	MMENumberForMTSMS        *string
	MMERegisteredForSMS      *bool
	SMSRegisterRequest       *int
	SMSRegistrationTimestamp *time.Time
	// Location fields decoded from User-Location-Info AVP (optional).
	MCC               *string
	MNC               *string
	TAC               *string
	ENodeBID          *string
	CellID            *string
	ECI               *string
	LocationTimestamp *time.Time
}

type ServingSGSNUpdate struct {
	ServingSGSN *string
	Timestamp   *time.Time
}

type ServingVLRUpdate struct {
	ServingVLR *string
	Timestamp  *time.Time
}

type ServingMSCUpdate struct {
	ServingMSC *string
	Timestamp  *time.Time
}

type ServingAMFUpdate struct {
	ServingAMF    *string
	AMFInstanceID *string
	Timestamp     *time.Time
}

type IMSSCSCFUpdate struct {
	SCSCF     *string
	Realm     *string
	Peer      *string
	Timestamp *time.Time
}

type IMSPCSCFUpdate struct {
	PCSCF     *string
	Realm     *string
	Peer      *string
	Timestamp *time.Time
}
