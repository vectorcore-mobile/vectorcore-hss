package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/models"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// The shared in-memory DB lives until its last connection closes; close
	// it so a repeated run (-count=N) starts empty.
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db, config.APIConfig{}, zap.NewNop())
}

func mustCreate(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	if err := db.Create(model).Error; err != nil {
		t.Fatalf("create %T: %v", model, err)
	}
}

func assertConflict(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected conflict error containing %q", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteAUCBlockedBySubscriber(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	auc := models.AUC{Ki: strings.Repeat("0", 32), OPc: strings.Repeat("1", 32), AMF: "8000", IMSI: ptr("001010000000001")}
	mustCreate(t, s.db, &auc)
	sub := models.Subscriber{IMSI: "001010000000001", AUCID: auc.AUCID, DefaultAPN: 0, APNList: ""}
	mustCreate(t, s.db, &sub)

	_, err := s.deleteAUC(ctx, &AUCIDInput{ID: auc.AUCID})
	assertConflict(t, err, "still used by subscriber")
}

func TestDeleteSubscriberBlockedByIMSSubscriber(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	sub := models.Subscriber{IMSI: "001010000000002", AUCID: 1, DefaultAPN: 0, APNList: ""}
	mustCreate(t, s.db, &sub)
	ims := models.IMSSubscriber{MSISDN: "15551230001", IMSI: ptr("001010000000002")}
	mustCreate(t, s.db, &ims)

	_, err := s.deleteSubscriber(ctx, &SubscriberIDInput{ID: sub.SubscriberID})
	assertConflict(t, err, "still used by IMS subscriber")
}

func TestDeleteSubscriberBlockedBySubscriberAttribute(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	sub := models.Subscriber{IMSI: "001010000000003", AUCID: 1, DefaultAPN: 0, APNList: ""}
	mustCreate(t, s.db, &sub)
	attr := models.SubscriberAttribute{SubscriberID: sub.SubscriberID, Key: "foo", Value: "bar"}
	mustCreate(t, s.db, &attr)

	_, err := s.deleteSubscriber(ctx, &SubscriberIDInput{ID: sub.SubscriberID})
	assertConflict(t, err, "still used by subscriber attribute")
}

func TestDeleteAlgorithmProfileBlockedByAUC(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	profile := models.AlgorithmProfile{ProfileName: "p1", C1: strings.Repeat("0", 32), C2: strings.Repeat("1", 32), C3: strings.Repeat("2", 32), C4: strings.Repeat("4", 32), C5: strings.Repeat("8", 32), R1: 64, R2: 0, R3: 32, R4: 64, R5: 96}
	mustCreate(t, s.db, &profile)
	auc := models.AUC{Ki: strings.Repeat("0", 32), OPc: strings.Repeat("1", 32), AMF: "8000", IMSI: ptr("001010000000004"), AlgorithmProfileID: ptr64(int64(profile.AlgorithmProfileID))}
	mustCreate(t, s.db, &auc)

	_, err := s.deleteAlgorithmProfile(ctx, &AlgorithmProfileIDInput{ID: profile.AlgorithmProfileID})
	assertConflict(t, err, "still used by AUC")
}

func TestDeleteIFCProfileBlockedByIMSSubscriber(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	ifc := models.IFCProfile{Name: "ifc-1", XMLData: "<ifc />"}
	mustCreate(t, s.db, &ifc)
	ims := models.IMSSubscriber{MSISDN: "15551230002", IFCProfileID: ptrInt(ifc.IFCProfileID)}
	mustCreate(t, s.db, &ims)

	_, err := s.deleteIFCProfile(ctx, &IFCProfileIDInput{ID: ifc.IFCProfileID})
	assertConflict(t, err, "still used by IMS subscriber")
}

func TestDeleteAPNBlockedBySubscriberList(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	apn := models.APN{APN: "internet", APNAMBRDown: 1, APNAMBRUp: 1}
	mustCreate(t, s.db, &apn)
	sub := models.Subscriber{IMSI: "001010000000005", AUCID: 1, DefaultAPN: 0, APNList: strconvI(apn.APNID)}
	mustCreate(t, s.db, &sub)

	_, err := s.deleteAPN(ctx, &APNIDInput{ID: apn.APNID})
	assertConflict(t, err, "still used by subscriber APN list")
}

func TestDeleteChargingRuleBlockedByAPN(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	rule := models.ChargingRule{RuleName: "gold", MBRDown: 1, MBRUp: 1, GBRDown: 1, GBRUp: 1}
	mustCreate(t, s.db, &rule)
	apn := models.APN{APN: "internet", APNAMBRDown: 1, APNAMBRUp: 1, ChargingRuleList: ptr(strconvI(rule.ChargingRuleID))}
	mustCreate(t, s.db, &apn)

	_, err := s.deleteChargingRule(ctx, &ChargingRuleIDInput{ID: rule.ChargingRuleID})
	assertConflict(t, err, "still used by APN")
}

func TestDeleteTFTBlockedByChargingRule(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	tft := models.TFT{TFTGroupID: 77, TFTString: "permit out ip from any to any", Direction: 1}
	mustCreate(t, s.db, &tft)
	rule := models.ChargingRule{RuleName: "gold", MBRDown: 1, MBRUp: 1, GBRDown: 1, GBRUp: 1, TFTGroupID: ptrInt(77)}
	mustCreate(t, s.db, &rule)

	_, err := s.deleteTFT(ctx, &TFTIDInput{ID: tft.TFTID})
	assertConflict(t, err, "still used by charging rule")
}

func TestDeleteAllowedWhenUnreferenced(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	apn := models.APN{APN: "unused", APNAMBRDown: 1, APNAMBRUp: 1}
	mustCreate(t, s.db, &apn)
	if _, err := s.deleteAPN(ctx, &APNIDInput{ID: apn.APNID}); err != nil {
		t.Fatalf("delete unreferenced apn: %v", err)
	}

	ifc := models.IFCProfile{Name: "unused-ifc", XMLData: "<ifc />"}
	mustCreate(t, s.db, &ifc)
	if _, err := s.deleteIFCProfile(ctx, &IFCProfileIDInput{ID: ifc.IFCProfileID}); err != nil {
		t.Fatalf("delete unreferenced ifc: %v", err)
	}
}

func ptr(s string) *string  { return &s }
func ptr64(v int64) *int64  { return &v }
func ptrInt(v int) *int     { return &v }
func strconvI(v int) string { return strconv.Itoa(v) }
