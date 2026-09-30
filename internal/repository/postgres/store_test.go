package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db, 2)
}

// TestGetAPNsByIDsBatches proves the batched lookup returns every requested
// APN in one call (the whole point of replacing ULR's per-APN N+1 loop).
func TestGetAPNsByIDsBatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, apn := range []models.APN{
		{APNID: 1, APN: "internet", APNAMBRDown: 1000, APNAMBRUp: 1000},
		{APNID: 2, APN: "ims", APNAMBRDown: 1000, APNAMBRUp: 1000},
		{APNID: 3, APN: "camera", APNAMBRDown: 1000, APNAMBRUp: 1000},
	} {
		if err := s.db.Create(&apn).Error; err != nil {
			t.Fatalf("seed apn %d: %v", apn.APNID, err)
		}
	}

	got, err := s.GetAPNsByIDs(ctx, []int{1, 3})
	if err != nil {
		t.Fatalf("GetAPNsByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d APNs, want 2", len(got))
	}
	names := map[string]bool{}
	for _, a := range got {
		names[a.APN] = true
	}
	if !names["internet"] || !names["camera"] {
		t.Errorf("got APNs %v, want internet and camera", names)
	}
}

// TestGetAPNByIDCachesAndInvalidates proves an update via InvalidateAPNCache
// is visible on the next read instead of serving a stale cached value — this
// is the property the REST API's APN update/delete handlers depend on.
func TestGetAPNByIDCachesAndInvalidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	apn := models.APN{APNID: 1, APN: "internet", APNAMBRDown: 1000, APNAMBRUp: 1000}
	if err := s.db.Create(&apn).Error; err != nil {
		t.Fatalf("seed apn: %v", err)
	}

	got, err := s.GetAPNByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetAPNByID: %v", err)
	}
	if got.APN != "internet" {
		t.Fatalf("got APN name %q, want internet", got.APN)
	}

	// Change the row directly (simulating the REST API's update handler)
	// without going through the cache.
	if err := s.db.Model(&models.APN{}).Where("apn_id = ?", 1).Update("apn", "internet-v2").Error; err != nil {
		t.Fatalf("update apn: %v", err)
	}

	stale, err := s.GetAPNByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetAPNByID (stale read): %v", err)
	}
	if stale.APN != "internet" {
		t.Fatalf("got %q before invalidation, want cached value internet", stale.APN)
	}

	s.InvalidateAPNCache(1)

	fresh, err := s.GetAPNByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetAPNByID (fresh read): %v", err)
	}
	if fresh.APN != "internet-v2" {
		t.Errorf("got %q after invalidation, want internet-v2", fresh.APN)
	}
}

// TestGetAPNsByIDsCachesAndInvalidates proves the batched path also serves
// from cache and respects invalidation for each id independently.
func TestGetAPNsByIDsCachesAndInvalidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, apn := range []models.APN{
		{APNID: 1, APN: "internet", APNAMBRDown: 1000, APNAMBRUp: 1000},
		{APNID: 2, APN: "ims", APNAMBRDown: 1000, APNAMBRUp: 1000},
	} {
		if err := s.db.Create(&apn).Error; err != nil {
			t.Fatalf("seed apn %d: %v", apn.APNID, err)
		}
	}

	if _, err := s.GetAPNsByIDs(ctx, []int{1, 2}); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	if err := s.db.Model(&models.APN{}).Where("apn_id = ?", 2).Update("apn", "ims-v2").Error; err != nil {
		t.Fatalf("update apn: %v", err)
	}
	s.InvalidateAPNCache(2)

	got, err := s.GetAPNsByIDs(ctx, []int{1, 2})
	if err != nil {
		t.Fatalf("GetAPNsByIDs: %v", err)
	}
	byID := map[int]string{}
	for _, a := range got {
		byID[a.APNID] = a.APN
	}
	if byID[1] != "internet" {
		t.Errorf("apn 1 = %q, want cached value internet", byID[1])
	}
	if byID[2] != "ims-v2" {
		t.Errorf("apn 2 = %q, want fresh value ims-v2 after invalidation", byID[2])
	}
}

// TestServingAPNByUEIPv6Prefix proves the Gx-stored /64 is found by the Rx
// lookup and that a re-CCR-I on the same PDN replaces the prefix (upsert).
func TestServingAPNByUEIPv6Prefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	str := func(v string) *string { return &v }

	rec := &models.ServingAPN{SubscriberID: 7, APNID: 2, APNName: "ims",
		PCRFSessionID: str("smf;1;51;app_gx"), UEIPv6Prefix: str("2001:db8:46:1a::/64")}
	if err := s.UpsertServingAPN(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetServingAPNByUEIPv6Prefix(ctx, "2001:db8:46:1a::/64")
	if err != nil || *got.PCRFSessionID != "smf;1;51;app_gx" {
		t.Fatalf("lookup got %+v err %v", got, err)
	}

	rec2 := &models.ServingAPN{SubscriberID: 7, APNID: 2, APNName: "ims",
		PCRFSessionID: str("smf;1;60;app_gx"), UEIPv6Prefix: str("2001:db8:46:2b::/64")}
	if err := s.UpsertServingAPN(ctx, rec2); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if _, err := s.GetServingAPNByUEIPv6Prefix(ctx, "2001:db8:46:1a::/64"); err != repository.ErrNotFound {
		t.Fatalf("stale prefix lookup err %v, want ErrNotFound", err)
	}
	if got, err := s.GetServingAPNByUEIPv6Prefix(ctx, "2001:db8:46:2b::/64"); err != nil || *got.PCRFSessionID != "smf;1;60;app_gx" {
		t.Fatalf("new prefix lookup got %+v err %v", got, err)
	}
}
