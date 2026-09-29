package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"gorm.io/gorm"

	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/models"
)

type IMSSubscriberListInput struct {
	Search string `query:"search" doc:"Case-insensitive substring search on MSISDN, IMSI, or the linked HSS subscriber's Alias" default:""`
	Limit  int    `query:"limit"  doc:"Max rows; 0 = no limit"                              default:"0"  minimum:"0"`
	Offset int    `query:"offset" doc:"Rows to skip"                                        default:"0"  minimum:"0"`
}

// IMSSubscriberWithAlias adds the Alias of the linked HSS subscriber (matched
// by IMSI) to an IMS subscriber row. Alias itself lives on models.Subscriber,
// not models.IMSSubscriber — there is no guaranteed HSS subscriber for a
// given IMS subscriber, so this is a best-effort lookup, not a real join key.
type IMSSubscriberWithAlias struct {
	models.IMSSubscriber
	Alias *string `json:"alias,omitempty"`
}
type IMSSubscriberListBody struct {
	Total int64                    `json:"total"`
	Items []IMSSubscriberWithAlias `json:"items"`
}
type IMSSubscriberListOutput struct{ Body IMSSubscriberListBody }
type IMSSubscriberOutput struct{ Body *models.IMSSubscriber }
type IMSSubscriberIDInput struct {
	ID int `path:"id"`
}
type IMSSubscriberIMSIInput struct {
	IMSI string `path:"imsi"`
}
type IMSSubscriberCreateInput struct{ Body *models.IMSSubscriber }
type IMSSubscriberUpdateInput struct {
	ID   int `path:"id"`
	Body *models.IMSSubscriber
}

func registerIMSSubscriberRoutes(s *Server, api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "list-ims-subscriber", Method: http.MethodGet, Path: "/ims_subscriber", Summary: "List IMS Subscribers", Tags: []string{"IMS Subscriber"}}, s.listIMSSubscribers)
	huma.Register(api, huma.Operation{OperationID: "create-ims-subscriber", Method: http.MethodPost, Path: "/ims_subscriber", Summary: "Create IMS Subscriber", Tags: []string{"IMS Subscriber"}, DefaultStatus: http.StatusCreated}, s.createIMSSubscriber)
	huma.Register(api, huma.Operation{OperationID: "get-ims-subscriber", Method: http.MethodGet, Path: "/ims_subscriber/{id}", Summary: "Get IMS Subscriber", Tags: []string{"IMS Subscriber"}}, s.getIMSSubscriber)
	huma.Register(api, huma.Operation{OperationID: "get-ims-subscriber-by-imsi", Method: http.MethodGet, Path: "/ims_subscriber/imsi/{imsi}", Summary: "Get IMS Subscriber by IMSI", Tags: []string{"IMS Subscriber"}}, s.getIMSSubscriberByIMSI)
	huma.Register(api, huma.Operation{OperationID: "update-ims-subscriber", Method: http.MethodPut, Path: "/ims_subscriber/{id}", Summary: "Update IMS Subscriber", Tags: []string{"IMS Subscriber"}}, s.updateIMSSubscriber)
	huma.Register(api, huma.Operation{OperationID: "delete-ims-subscriber", Method: http.MethodDelete, Path: "/ims_subscriber/{id}", Summary: "Delete IMS Subscriber", Tags: []string{"IMS Subscriber"}}, s.deleteIMSSubscriber)
}

func (s *Server) listIMSSubscribers(ctx context.Context, input *IMSSubscriberListInput) (*IMSSubscriberListOutput, error) {
	q := s.db.WithContext(ctx).Model(&models.IMSSubscriber{})
	if input.Search != "" {
		like := "%" + strings.ToLower(input.Search) + "%"
		q = q.Where(
			"LOWER(msisdn) LIKE ? OR LOWER(imsi) LIKE ? OR imsi IN (?)",
			like, like,
			s.db.WithContext(ctx).Model(&models.Subscriber{}).Select("imsi").Where("imsi IS NOT NULL AND LOWER(alias) LIKE ?", like),
		)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if input.Limit > 0 {
		q = q.Limit(input.Limit).Offset(input.Offset)
	}
	var items []models.IMSSubscriber
	if err := q.Find(&items).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if items == nil {
		items = []models.IMSSubscriber{}
	}

	// Alias lives on the HSS subscriber row, not IMS subscriber — resolve it
	// for just the IMSIs on this page rather than joining the main query.
	imsis := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.IMSI != nil && *it.IMSI != "" && !seen[*it.IMSI] {
			seen[*it.IMSI] = true
			imsis = append(imsis, *it.IMSI)
		}
	}
	aliasByIMSI := map[string]*string{}
	if len(imsis) > 0 {
		var subs []models.Subscriber
		if err := s.db.WithContext(ctx).Select("imsi", "alias").Where("imsi IN ?", imsis).Find(&subs).Error; err != nil {
			return nil, huma.Error500InternalServerError("db error", err)
		}
		for _, sub := range subs {
			aliasByIMSI[sub.IMSI] = sub.Alias
		}
	}

	out := make([]IMSSubscriberWithAlias, len(items))
	for i, it := range items {
		out[i] = IMSSubscriberWithAlias{IMSSubscriber: it}
		if it.IMSI != nil {
			out[i].Alias = aliasByIMSI[*it.IMSI]
		}
	}
	return &IMSSubscriberListOutput{Body: IMSSubscriberListBody{Total: total, Items: out}}, nil
}

func (s *Server) createIMSSubscriber(ctx context.Context, input *IMSSubscriberCreateInput) (*IMSSubscriberOutput, error) {
	msisdn, err := normalizeProvisionedMSISDN(input.Body.MSISDN)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	input.Body.MSISDN = msisdn
	input.Body.LastModified = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.WithContext(ctx).Create(input.Body).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.geored != nil {
		s.geored.PublishOAMPut(geored.EventIMSSubPut, input.Body)
	}
	return &IMSSubscriberOutput{Body: input.Body}, nil
}

func (s *Server) getIMSSubscriber(ctx context.Context, input *IMSSubscriberIDInput) (*IMSSubscriberOutput, error) {
	var item models.IMSSubscriber
	if err := s.db.WithContext(ctx).First(&item, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	return &IMSSubscriberOutput{Body: &item}, nil
}

func (s *Server) getIMSSubscriberByIMSI(ctx context.Context, input *IMSSubscriberIMSIInput) (*IMSSubscriberOutput, error) {
	var item models.IMSSubscriber
	if err := s.db.WithContext(ctx).Where("imsi = ?", input.IMSI).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	return &IMSSubscriberOutput{Body: &item}, nil
}

func (s *Server) updateIMSSubscriber(ctx context.Context, input *IMSSubscriberUpdateInput) (*IMSSubscriberOutput, error) {
	if err := s.db.WithContext(ctx).First(&models.IMSSubscriber{}, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	msisdn, err := normalizeProvisionedMSISDN(input.Body.MSISDN)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error(), err)
	}
	input.Body.MSISDN = msisdn
	input.Body.LastModified = time.Now().UTC().Format(time.RFC3339)
	input.Body.IMSSubscriberID = input.ID
	if err := s.db.WithContext(ctx).Save(input.Body).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.geored != nil {
		s.geored.PublishOAMPut(geored.EventIMSSubPut, input.Body)
	}
	return s.getIMSSubscriber(ctx, &IMSSubscriberIDInput{ID: input.ID})
}

func (s *Server) deleteIMSSubscriber(ctx context.Context, input *IMSSubscriberIDInput) (*struct{}, error) {
	var item models.IMSSubscriber
	if err := s.db.WithContext(ctx).First(&item, input.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, huma.Error404NotFound("not found", err)
		}
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if err := s.db.WithContext(ctx).Delete(&item).Error; err != nil {
		return nil, huma.Error500InternalServerError("db error", err)
	}
	if s.geored != nil {
		s.geored.PublishOAMDel(geored.EventIMSSubDel, item.MSISDN)
	}
	return nil, nil
}
