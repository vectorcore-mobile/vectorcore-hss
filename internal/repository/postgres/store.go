package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/svinson1121/vectorcore-hss/internal/metrics"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

type Store struct {
	db                 *gorm.DB
	eirNoMatchResponse int
	subCache           sync.Map
	aucCache           sync.Map
	apnCache           sync.Map
}

var _ repository.Repository = (*Store)(nil)

func New(db *gorm.DB, eirNoMatchResponse int) *Store {
	registerMetricsCallbacks(db)
	return &Store{db: db, eirNoMatchResponse: eirNoMatchResponse}
}

// registerMetricsCallbacks installs GORM before/after hooks that record query
// duration into the hss_db_query_duration_seconds histogram.
func registerMetricsCallbacks(db *gorm.DB) {
	type startKey struct{}

	before := func(op string) func(*gorm.DB) {
		return func(d *gorm.DB) {
			d.InstanceSet("hss:start", time.Now())
			d.InstanceSet("hss:op", op)
		}
	}
	after := func(d *gorm.DB) {
		v, ok := d.InstanceGet("hss:start")
		if !ok {
			return
		}
		op, _ := d.InstanceGet("hss:op")
		table := d.Statement.Table
		if table == "" {
			table = "unknown"
		}
		metrics.DBQueryDuration.WithLabelValues(op.(string), table).
			Observe(time.Since(v.(time.Time)).Seconds())
	}

	db.Callback().Query().Before("gorm:query").Register("hss:before_query", before("query"))
	db.Callback().Query().After("gorm:query").Register("hss:after_query", after)
	db.Callback().Create().Before("gorm:create").Register("hss:before_create", before("create"))
	db.Callback().Create().After("gorm:create").Register("hss:after_create", after)
	db.Callback().Update().Before("gorm:update").Register("hss:before_update", before("update"))
	db.Callback().Update().After("gorm:update").Register("hss:after_update", after)
	db.Callback().Delete().Before("gorm:delete").Register("hss:before_delete", before("delete"))
	db.Callback().Delete().After("gorm:delete").Register("hss:after_delete", after)
	db.Callback().Raw().Before("gorm:raw").Register("hss:before_raw", before("raw"))
	db.Callback().Raw().After("gorm:raw").Register("hss:after_raw", after)
}

func (s *Store) InvalidateCache(imsi string) {
	s.subCache.Delete(imsi)
	s.aucCache.Delete(imsi)
}

func (s *Store) GetAUCByIMSI(ctx context.Context, imsi string) (*models.AUC, error) {
	if v, ok := s.aucCache.Load(imsi); ok {
		e := v.(*cacheEntry)
		if time.Now().Before(e.expiresAt) {
			metrics.SubscriberCacheHits.WithLabelValues("auc", "hit").Inc()
			return e.value.(*models.AUC), nil
		}
		s.aucCache.Delete(imsi)
	}
	metrics.SubscriberCacheHits.WithLabelValues("auc", "miss").Inc()
	var auc models.AUC
	if err := s.db.WithContext(ctx).Where("imsi = ?", imsi).First(&auc).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	s.aucCache.Store(imsi, &cacheEntry{value: &auc, expiresAt: time.Now().Add(60 * time.Second)})
	return &auc, nil
}

func (s *Store) GetAUCByID(ctx context.Context, aucID int) (*models.AUC, error) {
	var auc models.AUC
	if err := s.db.WithContext(ctx).First(&auc, aucID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &auc, nil
}

func (s *Store) GetAlgorithmProfile(ctx context.Context, id int64) (*models.AlgorithmProfile, error) {
	var p models.AlgorithmProfile
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) AtomicGetAndIncrementSQN(ctx context.Context, aucID int, delta int64) (*models.AUC, error) {
	var before models.AUC
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&before, aucID).Error; err != nil {
			return err
		}
		return tx.Model(&before).Update("sqn", before.SQN+delta).Error
	})
	if err != nil {
		return nil, err
	}
	if before.IMSI != nil {
		s.InvalidateCache(*before.IMSI)
	}
	return &before, nil
}

func (s *Store) ResyncSQN(ctx context.Context, aucID int, newSQN int64) error {
	auc, err := s.GetAUCByID(ctx, aucID)
	if err != nil {
		return err
	}
	if auc.IMSI != nil {
		s.InvalidateCache(*auc.IMSI)
	}
	return s.db.WithContext(ctx).Model(&models.AUC{}).Where("auc_id = ?", aucID).Update("sqn", newSQN).Error
}

// apnCacheTTL is longer than subCache/aucCache's 60s: APN records are
// operator-managed config (APN-AMBR, QCI, charging characteristics, ...)
// that changes far less often than subscriber runtime state, and every
// write path invalidates the affected entry immediately via
// InvalidateAPNCache, so staleness beyond the TTL is bounded regardless.
const apnCacheTTL = 5 * time.Minute

func (s *Store) GetAPNByID(ctx context.Context, apnID int) (*models.APN, error) {
	if v, ok := s.apnCache.Load(apnID); ok {
		e := v.(*cacheEntry)
		if time.Now().Before(e.expiresAt) {
			metrics.SubscriberCacheHits.WithLabelValues("apn", "hit").Inc()
			return e.value.(*models.APN), nil
		}
		s.apnCache.Delete(apnID)
	}
	metrics.SubscriberCacheHits.WithLabelValues("apn", "miss").Inc()
	var apn models.APN
	if err := s.db.WithContext(ctx).First(&apn, apnID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	s.apnCache.Store(apnID, &cacheEntry{value: &apn, expiresAt: time.Now().Add(apnCacheTTL)})
	return &apn, nil
}

// GetAPNsByIDs serves as many ids as possible from apnCache and issues a
// single batched query for the remainder, so a ULR for a subscriber whose
// APNs were already resolved recently (the common case under sustained
// traffic) touches the database not at all.
func (s *Store) GetAPNsByIDs(ctx context.Context, ids []int) ([]models.APN, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]models.APN, 0, len(ids))
	var misses []int
	now := time.Now()
	for _, id := range ids {
		v, ok := s.apnCache.Load(id)
		if !ok {
			misses = append(misses, id)
			continue
		}
		e := v.(*cacheEntry)
		if now.After(e.expiresAt) {
			s.apnCache.Delete(id)
			misses = append(misses, id)
			continue
		}
		out = append(out, *e.value.(*models.APN))
	}
	metrics.SubscriberCacheHits.WithLabelValues("apn", "hit").Add(float64(len(out)))
	metrics.SubscriberCacheHits.WithLabelValues("apn", "miss").Add(float64(len(misses)))
	if len(misses) == 0 {
		return out, nil
	}
	var fetched []models.APN
	if err := s.db.WithContext(ctx).Where("apn_id IN ?", misses).Find(&fetched).Error; err != nil {
		return nil, err
	}
	for i := range fetched {
		a := fetched[i]
		s.apnCache.Store(a.APNID, &cacheEntry{value: &a, expiresAt: now.Add(apnCacheTTL)})
		out = append(out, a)
	}
	return out, nil
}

// InvalidateAPNCache must be called by every APN write path (create,
// update, delete) so a config change made via the REST API is visible to
// the next S6a/Gx request rather than serving a stale apnCache entry for up
// to apnCacheTTL.
func (s *Store) InvalidateAPNCache(apnID int) {
	s.apnCache.Delete(apnID)
}

func (s *Store) GetSubscriberByIMSI(ctx context.Context, imsi string) (*models.Subscriber, error) {
	if v, ok := s.subCache.Load(imsi); ok {
		e := v.(*cacheEntry)
		if time.Now().Before(e.expiresAt) {
			metrics.SubscriberCacheHits.WithLabelValues("subscriber", "hit").Inc()
			return e.value.(*models.Subscriber), nil
		}
		s.subCache.Delete(imsi)
	}
	metrics.SubscriberCacheHits.WithLabelValues("subscriber", "miss").Inc()
	var sub models.Subscriber
	if err := s.db.WithContext(ctx).Where("imsi = ?", imsi).First(&sub).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	s.subCache.Store(imsi, &cacheEntry{value: &sub, expiresAt: time.Now().Add(60 * time.Second)})
	return &sub, nil
}

func (s *Store) GetSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.Subscriber, error) {
	metrics.SubscriberCacheHits.WithLabelValues("subscriber", "miss").Inc()
	var sub models.Subscriber
	if err := s.db.WithContext(ctx).Where("msisdn = ?", msisdn).First(&sub).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &sub, nil
}

func (s *Store) UpdateServingSGSN(ctx context.Context, imsi string, update *repository.ServingSGSNUpdate) error {
	s.InvalidateCache(imsi)
	updates := map[string]interface{}{
		"serving_sgsn":           update.ServingSGSN,
		"serving_sgsn_timestamp": update.Timestamp,
	}
	return s.db.WithContext(ctx).Model(&models.Subscriber{}).Where("imsi = ?", imsi).Updates(updates).Error
}

func (s *Store) UpdateServingVLR(ctx context.Context, imsi string, update *repository.ServingVLRUpdate) error {
	s.InvalidateCache(imsi)
	updates := map[string]interface{}{
		"serving_vlr":           update.ServingVLR,
		"serving_vlr_timestamp": update.Timestamp,
	}
	return s.db.WithContext(ctx).Model(&models.Subscriber{}).Where("imsi = ?", imsi).Updates(updates).Error
}

func (s *Store) UpdateServingMSC(ctx context.Context, imsi string, update *repository.ServingMSCUpdate) error {
	s.InvalidateCache(imsi)
	updates := map[string]interface{}{
		"serving_msc":           update.ServingMSC,
		"serving_msc_timestamp": update.Timestamp,
	}
	return s.db.WithContext(ctx).Model(&models.Subscriber{}).Where("imsi = ?", imsi).Updates(updates).Error
}

func (s *Store) UpdateServingAMF(ctx context.Context, imsi string, update *repository.ServingAMFUpdate) error {
	s.InvalidateCache(imsi)
	updates := map[string]interface{}{
		"serving_amf":             update.ServingAMF,
		"serving_amf_timestamp":   update.Timestamp,
		"serving_amf_instance_id": update.AMFInstanceID,
	}
	return s.db.WithContext(ctx).Model(&models.Subscriber{}).Where("imsi = ?", imsi).Updates(updates).Error
}

func (s *Store) UpsertServingPDUSession(ctx context.Context, rec *models.ServingPDUSession) error {
	return s.db.WithContext(ctx).
		Where(models.ServingPDUSession{IMSI: rec.IMSI, PDUSessionID: rec.PDUSessionID}).
		Assign(*rec).
		FirstOrCreate(rec).Error
}

func (s *Store) DeleteServingPDUSession(ctx context.Context, imsi string, pduSessionID int) error {
	return s.db.WithContext(ctx).
		Where("imsi = ? AND pdu_session_id = ?", imsi, pduSessionID).
		Delete(&models.ServingPDUSession{}).Error
}

func (s *Store) ListServingPDUSessions(ctx context.Context, imsi string) ([]models.ServingPDUSession, error) {
	var rows []models.ServingPDUSession
	err := s.db.WithContext(ctx).Where("imsi = ?", imsi).Find(&rows).Error
	return rows, err
}

func (s *Store) EIRNoMatchResponse() int { return s.eirNoMatchResponse }

func (s *Store) ListEIR(ctx context.Context, out *[]models.EIR) error {
	return s.db.WithContext(ctx).Find(out).Error
}

func (s *Store) UpsertIMSIIMEIHistory(ctx context.Context, imsi, imei, devMake, devModel string, matchResponseCode int) error {
	now := time.Now().UTC()
	record := models.IMSIIMEIHistory{
		IMSI:              imsi,
		IMEI:              imei,
		MatchResponseCode: matchResponseCode,
		Make:              devMake,
		Model:             devModel,
		IMSIIMEITimestamp: &now,
		LastModified:      now.Format(time.RFC3339),
	}
	return s.db.WithContext(ctx).
		Where(models.IMSIIMEIHistory{IMSI: imsi, IMEI: imei}).
		Assign(models.IMSIIMEIHistory{
			MatchResponseCode: matchResponseCode,
			Make:              devMake,
			Model:             devModel,
			IMSIIMEITimestamp: &now,
			LastModified:      now.Format(time.RFC3339),
		}).
		FirstOrCreate(&record).Error
}

func (s *Store) UpdateServingMME(ctx context.Context, imsi string, update *repository.ServingMMEUpdate) error {
	s.InvalidateCache(imsi)
	updates := map[string]interface{}{
		"serving_mme":                update.ServingMME,
		"serving_mme_realm":          update.Realm,
		"serving_mme_peer":           update.Peer,
		"serving_mme_timestamp":      update.Timestamp,
		"mme_number_for_mt_sms":      update.MMENumberForMTSMS,
		"mme_registered_for_sms":     update.MMERegisteredForSMS,
		"sms_register_request":       update.SMSRegisterRequest,
		"sms_registration_timestamp": update.SMSRegistrationTimestamp,
	}
	if update.MCC != nil {
		updates["last_seen_mcc"] = update.MCC
		updates["last_seen_mnc"] = update.MNC
		updates["last_seen_tac"] = update.TAC
		updates["last_seen_enodeb_id"] = update.ENodeBID
		updates["last_seen_cell_id"] = update.CellID
		updates["last_seen_eci"] = update.ECI
		updates["last_location_update_timestamp"] = update.LocationTimestamp
	}
	return s.db.WithContext(ctx).Model(&models.Subscriber{}).Where("imsi = ?", imsi).Updates(updates).Error
}

func (s *Store) GetIMSSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.IMSSubscriber, error) {
	var sub models.IMSSubscriber
	if err := s.db.WithContext(ctx).Where("msisdn = ?", msisdn).First(&sub).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &sub, nil
}

func (s *Store) GetIMSSubscriberByIMSI(ctx context.Context, imsi string) (*models.IMSSubscriber, error) {
	var sub models.IMSSubscriber
	if err := s.db.WithContext(ctx).Where("imsi = ?", imsi).First(&sub).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &sub, nil
}

func (s *Store) UpdateIMSSCSCF(ctx context.Context, msisdn string, update *repository.IMSSCSCFUpdate) error {
	updates := map[string]interface{}{
		"scscf":           update.SCSCF,
		"scscf_realm":     update.Realm,
		"scscf_peer":      update.Peer,
		"scscf_timestamp": update.Timestamp,
	}
	return s.db.WithContext(ctx).Model(&models.IMSSubscriber{}).Where("msisdn = ?", msisdn).Updates(updates).Error
}

func (s *Store) UpdateIMSPCSCF(ctx context.Context, msisdn string, update *repository.IMSPCSCFUpdate) error {
	updates := map[string]interface{}{
		"pcscf":           update.PCSCF,
		"pcscf_realm":     update.Realm,
		"pcscf_peer":      update.Peer,
		"pcscf_timestamp": update.Timestamp,
	}
	return s.db.WithContext(ctx).Model(&models.IMSSubscriber{}).Where("msisdn = ?", msisdn).Updates(updates).Error
}

func (s *Store) GetIFCProfileByID(ctx context.Context, id int) (*models.IFCProfile, error) {
	var p models.IFCProfile
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetAPNByName(ctx context.Context, apnName string) (*models.APN, error) {
	var a models.APN
	if err := s.db.WithContext(ctx).Where("apn = ?", apnName).First(&a).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) UpsertServingAPN(ctx context.Context, record *models.ServingAPN) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "subscriber_id"}, {Name: "apn"}},
			DoUpdates: clause.AssignmentColumns([]string{"apn_name", "pcrf_session_id", "ip_version", "ue_ip", "ue_ipv6_prefix", "serving_pgw", "serving_pgw_timestamp", "serving_pgw_realm", "serving_pgw_peer", "last_modified"}),
		}).
		Create(record).Error
}

func (s *Store) UpsertEmergencySubscriber(ctx context.Context, rec *models.EmergencySubscriber) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "imsi"}},
			DoUpdates: clause.AssignmentColumns([]string{"serving_pgw", "serving_pgw_timestamp", "gx_origin_realm", "gx_origin_host", "rat_type", "ip", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteEmergencySubscriberByIMSI(ctx context.Context, imsi string) error {
	return s.db.WithContext(ctx).
		Where("imsi = ?", imsi).
		Delete(&models.EmergencySubscriber{}).Error
}

func (s *Store) DeleteServingAPNBySession(ctx context.Context, pcrfSessionID string) error {
	return s.db.WithContext(ctx).
		Where("pcrf_session_id = ?", pcrfSessionID).
		Delete(&models.ServingAPN{}).Error
}

func (s *Store) GetServingAPNBySession(ctx context.Context, pcrfSessionID string) (*models.ServingAPN, error) {
	var rec models.ServingAPN
	if err := s.db.WithContext(ctx).Where("pcrf_session_id = ?", pcrfSessionID).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) GetServingAPNByIMSI(ctx context.Context, imsi string) (*models.ServingAPN, error) {
	var sub models.Subscriber
	if err := s.db.WithContext(ctx).Where("imsi = ?", imsi).First(&sub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	var rec models.ServingAPN
	if err := s.db.WithContext(ctx).
		Where("subscriber_id = ? AND pcrf_session_id IS NOT NULL AND serving_pgw_peer IS NOT NULL", sub.SubscriberID).
		First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) GetServingAPNByMSISDN(ctx context.Context, msisdn string) (*models.ServingAPN, error) {
	var sub models.Subscriber
	if err := s.db.WithContext(ctx).Where("msisdn = ?", msisdn).First(&sub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	var rec models.ServingAPN
	if err := s.db.WithContext(ctx).
		Where("subscriber_id = ? AND pcrf_session_id IS NOT NULL AND serving_pgw_peer IS NOT NULL", sub.SubscriberID).
		First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) GetServingAPNByIdentity(ctx context.Context, identity string) (*models.ServingAPN, error) {
	var rec models.ServingAPN
	err := s.db.WithContext(ctx).
		Joins("JOIN subscriber ON subscriber.subscriber_id = serving_apn.subscriber_id").
		Where("(subscriber.imsi = ? OR subscriber.msisdn = ?) AND serving_apn.pcrf_session_id IS NOT NULL AND serving_apn.serving_pgw_peer IS NOT NULL", identity, identity).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, repository.ErrNotFound
	}
	return &rec, err
}

func (s *Store) GetServingAPNByUEIP(ctx context.Context, ueIP string) (*models.ServingAPN, error) {
	var rec models.ServingAPN
	if err := s.db.WithContext(ctx).Where("ue_ip = ?", ueIP).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) GetServingAPNByUEIPv6Prefix(ctx context.Context, prefix string) (*models.ServingAPN, error) {
	var rec models.ServingAPN
	if err := s.db.WithContext(ctx).Where("ue_ipv6_prefix = ?", prefix).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

func (s *Store) GetAllChargingRules(ctx context.Context) ([]models.ChargingRule, error) {
	var rules []models.ChargingRule
	if err := s.db.WithContext(ctx).Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *Store) GetChargingRulesByNames(ctx context.Context, names []string) ([]models.ChargingRule, error) {
	var rules []models.ChargingRule
	if err := s.db.WithContext(ctx).Where("rule_name IN ?", names).Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *Store) GetChargingRulesByIDs(ctx context.Context, ids []int) ([]models.ChargingRule, error) {
	var rules []models.ChargingRule
	if err := s.db.WithContext(ctx).Where("charging_rule_id IN ?", ids).Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *Store) GetTFTsByGroupID(ctx context.Context, groupID int) ([]models.TFT, error) {
	var tfts []models.TFT
	if err := s.db.WithContext(ctx).Where("tft_group_id = ?", groupID).Find(&tfts).Error; err != nil {
		return nil, err
	}
	return tfts, nil
}

func (s *Store) GetSubscriberRoutingBySubscriberAndAPN(ctx context.Context, subscriberID, apnID int) (*models.SubscriberRouting, error) {
	var r models.SubscriberRouting
	if err := s.db.WithContext(ctx).Where("subscriber_id = ? AND apn_id = ?", subscriberID, apnID).First(&r).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetSubscriberRoutingsBySubscriberAndAPNs(ctx context.Context, subscriberID int, apnIDs []int) ([]models.SubscriberRouting, error) {
	if len(apnIDs) == 0 {
		return nil, nil
	}
	var routings []models.SubscriberRouting
	if err := s.db.WithContext(ctx).Where("subscriber_id = ? AND apn_id IN ?", subscriberID, apnIDs).Find(&routings).Error; err != nil {
		return nil, err
	}
	return routings, nil
}

func (s *Store) GetRoamingRuleByMCCMNC(ctx context.Context, mcc, mnc string) (*models.RoamingRules, error) {
	var rule models.RoamingRules
	if err := s.db.WithContext(ctx).Where("mcc = ? AND mnc = ? AND enabled = true", mcc, mnc).First(&rule).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

// ── GeoRed — snapshot reads ───────────────────────────────────────────────────

func (s *Store) ListAllAUC(ctx context.Context) ([]models.AUC, error) {
	var out []models.AUC
	return out, s.db.WithContext(ctx).Find(&out).Error
}

func (s *Store) ListAllSubscribers(ctx context.Context) ([]models.Subscriber, error) {
	var out []models.Subscriber
	return out, s.db.WithContext(ctx).Find(&out).Error
}

func (s *Store) ListAllIMSSubscribers(ctx context.Context) ([]models.IMSSubscriber, error) {
	var out []models.IMSSubscriber
	return out, s.db.WithContext(ctx).Find(&out).Error
}

func (s *Store) ListAllServingAPN(ctx context.Context) ([]repository.GeoredServingAPN, error) {
	type row struct {
		PCRFSessionID *string
		IMSI          string
		MSISDN        *string
		APNID         *int
		APNName       *string
		PGWIP         *string
		UEIP          *string
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Table("serving_apn sa").
		Select("sa.pcrf_session_id, sub.imsi, sub.msisdn, sa.apn as apn_id, sa.apn_name, sa.serving_pgw as pgwip, sa.ue_ip").
		Joins("JOIN subscriber sub ON sub.subscriber_id = sa.subscriber_id").
		Where("sa.pcrf_session_id IS NOT NULL").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]repository.GeoredServingAPN, 0, len(rows))
	for _, r := range rows {
		if r.PCRFSessionID == nil {
			continue
		}
		apnID := r.APNID
		apnName := r.APNName
		out = append(out, repository.GeoredServingAPN{
			PCRFSessionID: *r.PCRFSessionID,
			IMSI:          r.IMSI,
			MSISDN:        r.MSISDN,
			APNID:         apnID,
			APNName:       apnName,
			PGWIP:         r.PGWIP,
			UEIP:          r.UEIP,
		})
	}
	return out, nil
}

// ── GeoRed — OAM record apply ─────────────────────────────────────────────────

func (s *Store) UpsertSubscriber(ctx context.Context, rec *models.Subscriber) error {
	s.InvalidateCache(rec.IMSI)
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "imsi"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "auc_id", "default_apn", "apn_list", "msisdn", "alias", "ue_ambr_dl", "ue_ambr_ul", "nam", "access_restriction_data", "roaming_enabled", "subscribed_rau_tau_timer", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteSubscriberByIMSI(ctx context.Context, imsi string) error {
	s.InvalidateCache(imsi)
	return s.db.WithContext(ctx).Where("imsi = ?", imsi).Delete(&models.Subscriber{}).Error
}

func (s *Store) UpsertAUC(ctx context.Context, rec *models.AUC) error {
	if rec.IMSI != nil {
		s.InvalidateCache(*rec.IMSI)
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "auc_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"ki", "opc", "amf", "iccid", "imsi", "batch_name", "sim_vendor", "esim", "lpa", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteAUCByID(ctx context.Context, id int) error {
	return s.db.WithContext(ctx).Delete(&models.AUC{}, id).Error
}

func (s *Store) UpsertAPN(ctx context.Context, rec *models.APN) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "apn_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"apn", "ip_version", "pgw_address", "sgw_address", "charging_characteristics", "apn_ambr_dl", "apn_ambr_ul", "qci", "arp_priority", "arp_preemption_capability", "arp_preemption_vulnerability", "charging_rule_list", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteAPNByID(ctx context.Context, id int) error {
	return s.db.WithContext(ctx).Delete(&models.APN{}, id).Error
}

func (s *Store) UpsertIMSSubscriber(ctx context.Context, rec *models.IMSSubscriber) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "msisdn"}},
			DoUpdates: clause.AssignmentColumns([]string{"imsi", "ifc_profile_id", "xcap_profile", "sh_profile", "sh_template_path", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteIMSSubscriberByMSISDN(ctx context.Context, msisdn string) error {
	return s.db.WithContext(ctx).Where("msisdn = ?", msisdn).Delete(&models.IMSSubscriber{}).Error
}

func (s *Store) UpsertEIR(ctx context.Context, rec *models.EIR) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "eir_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"imei", "imsi", "regex_mode", "match_response_code", "last_modified"}),
		}).
		Create(rec).Error
}

func (s *Store) DeleteEIRByID(ctx context.Context, id int) error {
	return s.db.WithContext(ctx).Delete(&models.EIR{}, id).Error
}

// ── MWD (S6c) ────────────────────────────────────────────────────────────────

func (s *Store) StoreMWD(ctx context.Context, rec *models.MessageWaitingData) error {
	if rec == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	record := *rec
	record.LastModified = now
	return s.db.WithContext(ctx).
		Where("imsi = ? AND sc_address = ?", record.IMSI, record.SCAddress).
		Assign(models.MessageWaitingData{
			SCOriginHost:           record.SCOriginHost,
			SCOriginRealm:          record.SCOriginRealm,
			SMRPMTI:                record.SMRPMTI,
			MWDStatusFlags:         record.MWDStatusFlags,
			SMSMICorrelationID:     record.SMSMICorrelationID,
			AbsentUserDiagnosticSM: record.AbsentUserDiagnosticSM,
			LastAlertTrigger:       record.LastAlertTrigger,
			LastAlertAttemptAt:     record.LastAlertAttemptAt,
			AlertAttemptCount:      record.AlertAttemptCount,
			LastModified:           now,
		}).
		FirstOrCreate(&record).Error
}

func (s *Store) GetMWDForIMSI(ctx context.Context, imsi string) ([]models.MessageWaitingData, error) {
	var records []models.MessageWaitingData
	if err := s.db.WithContext(ctx).Where("imsi = ?", imsi).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) DeleteMWD(ctx context.Context, imsi, scAddr string) error {
	return s.db.WithContext(ctx).
		Where("imsi = ? AND sc_address = ?", imsi, scAddr).
		Delete(&models.MessageWaitingData{}).Error
}
