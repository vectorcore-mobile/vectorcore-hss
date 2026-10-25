package policy

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	pgstore "github.com/svinson1121/vectorcore-hss/internal/repository/postgres"
)

func TestResolveSessionPolicy(t *testing.T) {
	store := newTestStore(t)

	rule := models.ChargingRule{
		RuleName:                   "video",
		QCI:                        2,
		ARPPriority:                11,
		ARPPreemptionCapability:    boolPtr(true),
		ARPPreemptionVulnerability: boolPtr(false),
		MBRDown:                    512000,
		MBRUp:                      128000,
		GBRDown:                    256000,
		GBRUp:                      64000,
		TFTGroupID:                 intPtr(77),
		Precedence:                 intPtr(30),
	}
	mustCreate(t, store.db, &rule)
	mustCreate(t, store.db, &models.TFT{
		TFTGroupID: 77, TFTString: "permit out ip from any to any", Direction: 1,
	})
	apn := models.APN{
		APN:                        "internet",
		APNAMBRDown:                99000000,
		APNAMBRUp:                  1500000,
		QCI:                        9,
		ARPPriority:                4,
		ARPPreemptionCapability:    boolPtr(false),
		ARPPreemptionVulnerability: boolPtr(true),
		ChargingRuleList:           strPtr(strconv.Itoa(rule.ChargingRuleID)),
	}
	mustCreate(t, store.db, &apn)
	mustCreate(t, store.db, &models.Subscriber{
		IMSI:       "001010000000001",
		AUCID:      1,
		DefaultAPN: apn.APNID,
		APNList:    strconv.Itoa(apn.APNID),
	})

	p, err := ResolveSessionPolicy(context.Background(), store, "001010000000001", "internet")
	if err != nil {
		t.Fatalf("ResolveSessionPolicy: %v", err)
	}
	if p.DNN != "internet" {
		t.Fatalf("DNN: got %q", p.DNN)
	}
	// DB bit rates are bps (as sent in S6a/Gx AVPs); SBI wants TS 29.571 strings.
	if p.SessionAMBRDownlink != "99 Mbps" {
		t.Fatalf("SessionAMBRDownlink: got %q", p.SessionAMBRDownlink)
	}
	if p.SessionAMBRUplink != "1500 Kbps" {
		t.Fatalf("SessionAMBRUplink: got %q", p.SessionAMBRUplink)
	}
	if len(p.Rules) != 1 {
		t.Fatalf("Rules len: got %d", len(p.Rules))
	}
	if r := p.Rules[0]; r.MaxBRDownlink != "512 Kbps" || r.MaxBRUplink != "128 Kbps" ||
		r.GuaranteedBRDL != "256 Kbps" || r.GuaranteedBRUL != "64 Kbps" {
		t.Fatalf("rule bit rates: got MBR %q/%q GBR %q/%q", r.MaxBRDownlink, r.MaxBRUplink, r.GuaranteedBRDL, r.GuaranteedBRUL)
	}
	if p.DefaultPreemptCap == nil || *p.DefaultPreemptCap {
		t.Fatalf("DefaultPreemptCap: got %#v", p.DefaultPreemptCap)
	}
	if p.DefaultPreemptVuln == nil || !*p.DefaultPreemptVuln {
		t.Fatalf("DefaultPreemptVuln: got %#v", p.DefaultPreemptVuln)
	}
	if p.Rules[0].ID != "video" {
		t.Fatalf("Rule ID: got %q", p.Rules[0].ID)
	}
	if p.Rules[0].PreemptCap == nil || !*p.Rules[0].PreemptCap {
		t.Fatalf("Rule PreemptCap: got %#v", p.Rules[0].PreemptCap)
	}
	if p.Rules[0].PreemptVuln == nil || *p.Rules[0].PreemptVuln {
		t.Fatalf("Rule PreemptVuln: got %#v", p.Rules[0].PreemptVuln)
	}
	if len(p.Rules[0].Flows) != 1 || !strings.Contains(p.Rules[0].Flows[0].Description, "permit out ip") {
		t.Fatalf("unexpected flows: %#v", p.Rules[0].Flows)
	}
	if p.Rules[0].Flows[0].Direction != 1 {
		t.Fatalf("unexpected flow direction: %#v", p.Rules[0].Flows[0])
	}
}

type testStore struct {
	*pgstore.Store
	db *gorm.DB
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &testStore{Store: pgstore.New(db, 2), db: db}
}

func mustCreate(t *testing.T, db *gorm.DB, model interface{}) {
	t.Helper()
	if err := db.Create(model).Error; err != nil {
		t.Fatalf("create %T: %v", model, err)
	}
}

func strPtr(v string) *string { return &v }
func intPtr(v int) *int       { return &v }
func boolPtr(v bool) *bool    { return &v }
