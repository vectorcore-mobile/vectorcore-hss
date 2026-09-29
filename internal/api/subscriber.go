package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/models"
)

type SubscriberListInput struct {
	Search string `query:"search" doc:"Case-insensitive substring search on IMSI, MSISDN, or Alias" default:""`
	Limit  int    `query:"limit"  doc:"Max rows; 0 = no limit"                              default:"0"  minimum:"0"`
	Offset int    `query:"offset" doc:"Rows to skip"                                        default:"0"  minimum:"0"`
}
type SubscriberListBody struct {
	Total int64               `json:"total"`
	Items []models.Subscriber `json:"items"`
}
type SubscriberListOutput struct{ Body SubscriberListBody }
type SubscriberOutput struct{ Body *models.Subscriber }
type SubscriberIDInput struct {
	ID int `path:"id"`
}
type SubscriberIMSIInput struct {
	IMSI string `path:"imsi"`
}
type SubscriberCreateInput struct{ Body *models.Subscriber }
type SubscriberUpdateInput struct {
	ID   int `path:"id"`
	Body *models.Subscriber
}

func registerSubscriberRoutes(s *Server, api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "list-subscriber", Method: http.MethodGet, Path: "/subscriber", Summary: "List Subscribers", Tags: []string{"Subscriber"}}, s.listSubscribers)
	huma.Register(api, huma.Operation{OperationID: "create-subscriber", Method: http.MethodPost, Path: "/subscriber", Summary: "Create Subscriber", Tags: []string{"Subscriber"}, DefaultStatus: http.StatusCreated}, s.createSubscriber)
	huma.Register(api, huma.Operation{OperationID: "get-subscriber", Method: http.MethodGet, Path: "/subscriber/{id}", Summary: "Get Subscriber", Tags: []string{"Subscriber"}}, s.getSubscriber)
	huma.Register(api, huma.Operation{OperationID: "get-subscriber-by-imsi", Method: http.MethodGet, Path: "/subscriber/imsi/{imsi}", Summary: "Get Subscriber by IMSI", Tags: []string{"Subscriber"}}, s.getSubscriberByIMSI)
	huma.Register(api, huma.Operation{OperationID: "update-subscriber", Method: http.MethodPut, Path: "/subscriber/{id}", Summary: "Update Subscriber", Tags: []string{"Subscriber"}}, s.updateSubscriber)
	huma.Register(api, huma.Operation{OperationID: "delete-subscriber", Method: http.MethodDelete, Path: "/subscriber/{id}", Summary: "Delete Subscriber", Tags: []string{"Subscriber"}}, s.deleteSubscriber)
}

func (s *Server) listSubscribers(ctx context.Context, input *SubscriberListInput) (*SubscriberListOutput, error) {
	q := s.db.WithContext(ctx).Model(&models.Subscriber{})
	if input.Search != "" {
		like := "%" + strings.ToLower(input.Search) + "%"
		q = q.Where("LOWER(imsi) LIKE ? OR LOWER(msisdn) LIKE ? OR LOWER(alias) LIKE ?", like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if input.Limit > 0 {
		q = q.Limit(input.Limit).Offset(input.Offset)
	}
	var items []models.Subscriber
	if err := q.Find(&items).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if items == nil {
		items = []models.Subscriber{}
	}
	return &SubscriberListOutput{Body: SubscriberListBody{Total: total, Items: items}}, nil
}

// validateSubscriberStatusODB enforces TS 29.272 §7.3.29/§7.3.30 constraints:
// Subscriber-Status must be a defined enum value, Operator-Determined-Barring
// must not contain reserved bits, and ODB flags are only meaningful while
// barring is active — they are cleared otherwise so no stale restriction can
// ever be emitted in Subscription-Data.
func validateSubscriberStatusODB(sub *models.Subscriber) error {
	switch sub.SubscriberStatus {
	case models.SubscriberStatusServiceGranted, models.SubscriberStatusOperatorDeterminedBarring:
	default:
		return fmt.Errorf("subscriber_status must be 0 (SERVICE_GRANTED) or 1 (OPERATOR_DETERMINED_BARRING), got %d", sub.SubscriberStatus)
	}
	if sub.OperatorDeterminedBarring&^models.ODBValidMask != 0 {
		return fmt.Errorf("operator_determined_barring contains undefined/reserved bits: 0x%08x", sub.OperatorDeterminedBarring)
	}
	if sub.SubscriberStatus == models.SubscriberStatusServiceGranted {
		sub.OperatorDeterminedBarring = 0
	}
	return nil
}

// validateAccessRestrictionData enforces the TS 29.272 §7.3.31 NOTE 2
// constraint: bits 11 (LTE-M Not Allowed) and 12 (WB-E-UTRAN Except LTE-M Not
// Allowed) are only meaningful when bit 4 (WB-E-UTRAN Not Allowed) is clear.
// If WB-E-UTRAN is already entirely prohibited, further restricting LTE-M
// within it is contradictory. Per the handoff, an invalid combination must be
// rejected rather than silently reinterpreted/cleared.
func validateAccessRestrictionData(sub *models.Subscriber) error {
	if sub.AccessRestrictionData == nil {
		return nil
	}
	ard := *sub.AccessRestrictionData
	if ard&models.ARDWBEUTRANNotAllowed != 0 && ard&(models.ARDLTEMNotAllowed|models.ARDWBEUTRANExceptLTEMNotAllowed) != 0 {
		return fmt.Errorf("access_restriction_data: WB-E-UTRAN Not Allowed (bit 4) cannot be combined with LTE-M Not Allowed (bit 11) or WB-E-UTRAN Except LTE-M Not Allowed (bit 12) — per TS 29.272 §7.3.31 NOTE 2, bits 11/12 are only used when bit 4 is not set, got 0x%08x", ard)
	}
	return nil
}

func (s *Server) createSubscriber(ctx context.Context, input *SubscriberCreateInput) (*SubscriberOutput, error) {
	if err := validateSubscriberStatusODB(input.Body); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	if err := validateAccessRestrictionData(input.Body); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	if err := normalizeProvisionedMSISDNPtr(input.Body.MSISDN); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	input.Body.LastModified = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.WithContext(ctx).Create(input.Body).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.geored != nil {
		s.geored.PublishOAMPut(geored.EventSubscriberPut, input.Body)
	}
	return &SubscriberOutput{Body: input.Body}, nil
}

func (s *Server) getSubscriber(ctx context.Context, input *SubscriberIDInput) (*SubscriberOutput, error) {
	var item models.Subscriber
	if err := s.db.WithContext(ctx).First(&item, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	return &SubscriberOutput{Body: &item}, nil
}

func (s *Server) getSubscriberByIMSI(ctx context.Context, input *SubscriberIMSIInput) (*SubscriberOutput, error) {
	var item models.Subscriber
	if err := s.db.WithContext(ctx).Where("imsi = ?", input.IMSI).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	return &SubscriberOutput{Body: &item}, nil
}

func (s *Server) updateSubscriber(ctx context.Context, input *SubscriberUpdateInput) (*SubscriberOutput, error) {
	var old models.Subscriber
	if err := s.db.WithContext(ctx).First(&old, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if err := validateSubscriberStatusODB(input.Body); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	if err := validateAccessRestrictionData(input.Body); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	if err := normalizeProvisionedMSISDNPtr(input.Body.MSISDN); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	input.Body.LastModified = time.Now().UTC().Format(time.RFC3339)
	input.Body.SubscriberID = input.ID
	// Preserve internal network-node state — written by Diameter/5G layers,
	// must not be zeroed out by a user-facing PUT that omits them.
	input.Body.ServingMME = old.ServingMME
	input.Body.ServingMMERealm = old.ServingMMERealm
	input.Body.ServingMMEPeer = old.ServingMMEPeer
	input.Body.ServingMMETimestamp = old.ServingMMETimestamp
	input.Body.ServingAMF = old.ServingAMF
	input.Body.ServingAMFTimestamp = old.ServingAMFTimestamp
	input.Body.ServingAMFInstanceID = old.ServingAMFInstanceID
	input.Body.ServingMSC = old.ServingMSC
	input.Body.ServingMSCTimestamp = old.ServingMSCTimestamp
	input.Body.ServingVLR = old.ServingVLR
	input.Body.ServingVLRTimestamp = old.ServingVLRTimestamp
	input.Body.ServingSGSN = old.ServingSGSN
	input.Body.ServingSGSNTimestamp = old.ServingSGSNTimestamp
	// subscriptionDataChanged tracks fields carried in S6a Subscription-Data
	// (Access-Restriction-Data, Subscriber-Status, Operator-Determined-Barring)
	// so an in-session subscriber can be updated via IDR without a fresh ULR.
	subscriptionDataChanged := !equalUint32Ptr(old.AccessRestrictionData, input.Body.AccessRestrictionData) ||
		old.SubscriberStatus != input.Body.SubscriberStatus ||
		old.OperatorDeterminedBarring != input.Body.OperatorDeterminedBarring
	if err := s.db.WithContext(ctx).Save(input.Body).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.cache != nil && input.Body.IMSI != "" {
		s.cache.InvalidateCache(input.Body.IMSI)
	}
	// If subscriber is being disabled (enabled true→false) and Diameter is wired,
	// send a CLR to force the UE to detach from its serving MME.
	wasEnabled := old.Enabled == nil || *old.Enabled
	nowDisabled := input.Body.Enabled != nil && !*input.Body.Enabled
	if wasEnabled && nowDisabled && s.clr != nil && input.Body.IMSI != "" {
		imsi := input.Body.IMSI
		go func() {
			if err := s.clr.SendCLRByIMSI(context.Background(), imsi); err != nil {
				s.log.Warn("api: CLR on subscriber disable failed",
					zap.String("imsi", imsi), zap.Error(err))
			}
		}()
	}
	if subscriptionDataChanged && s.idr != nil && input.Body.IMSI != "" && !nowDisabled {
		imsi := input.Body.IMSI
		go func() {
			if err := s.idr.SendIDRByIMSI(context.Background(), imsi); err != nil {
				s.log.Warn("api: IDR on Access-Restriction-Data change failed",
					zap.String("imsi", imsi), zap.Error(err))
			}
		}()
	}
	if s.geored != nil {
		s.geored.PublishOAMPut(geored.EventSubscriberPut, input.Body)
	}
	return s.getSubscriber(ctx, &SubscriberIDInput{ID: input.ID})
}

func equalUint32Ptr(a, b *uint32) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func (s *Server) deleteSubscriber(ctx context.Context, input *SubscriberIDInput) (*struct{}, error) {
	var sub models.Subscriber
	if err := s.db.WithContext(ctx).First(&sub, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if msisdn, err := firstString(ctx, s.db, &models.IMSSubscriber{}, "msisdn", "imsi = ?", sub.IMSI); err == nil {
		return nil, conflictInUse("subscriber", sub.IMSI, "IMS subscriber", msisdn)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if key, err := firstString(ctx, s.db, &models.SubscriberAttribute{}, "key", "subscriber_id = ?", sub.SubscriberID); err == nil {
		return nil, conflictInUse("subscriber", sub.IMSI, "subscriber attribute", key)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	var routing models.SubscriberRouting
	if err := s.db.WithContext(ctx).Where("subscriber_id = ?", sub.SubscriberID).Take(&routing).Error; err == nil {
		return nil, conflictInUse("subscriber", sub.IMSI, "subscriber routing", strconv.Itoa(routing.SubscriberRoutingID))
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if err := s.db.WithContext(ctx).Delete(&sub).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.cache != nil && sub.IMSI != "" {
		s.cache.InvalidateCache(sub.IMSI)
	}
	if s.geored != nil {
		s.geored.PublishOAMDel(geored.EventSubscriberDel, sub.IMSI)
	}
	return nil, nil
}
