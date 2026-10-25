package policy

import (
	"context"
	"strconv"
	"strings"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

type SessionPolicy struct {
	Supi                    string
	DNN                     string
	ChargingCharacteristics string
	SessionAMBRDownlink     string
	SessionAMBRUplink       string
	DefaultQos5QI           int
	DefaultARP              int
	DefaultPreemptCap       *bool
	DefaultPreemptVuln      *bool
	Rules                   []PolicyRule
}

type PolicyRule struct {
	ID                string
	Precedence        int
	QosReference      string
	ChargingReference string
	Flows             []PolicyFlow
	RatingGroup       *int
	MaxBRDownlink     string
	MaxBRUplink       string
	GuaranteedBRDL    string
	GuaranteedBRUL    string
	FiveQI            int
	ARP               int
	PreemptCap        *bool
	PreemptVuln       *bool
}

type PolicyFlow struct {
	Description string
	Direction   int
}

func ResolveSessionPolicy(ctx context.Context, store repository.Repository, supi, dnn string) (*SessionPolicy, error) {
	sub, err := store.GetSubscriberByIMSI(ctx, normalizeSUPI(supi))
	if err != nil {
		return nil, err
	}
	apn, err := store.GetAPNByName(ctx, normalizeDNN(dnn))
	if err != nil {
		return nil, err
	}

	policy := &SessionPolicy{
		Supi:                    sub.IMSI,
		DNN:                     apn.APN,
		ChargingCharacteristics: apn.ChargingCharacteristics,
		SessionAMBRDownlink:     sbi.FormatBitRate(apn.APNAMBRDown),
		SessionAMBRUplink:       sbi.FormatBitRate(apn.APNAMBRUp),
		DefaultQos5QI:           normalize5QI(apn.QCI),
		DefaultARP:              normalizeARP(apn.ARPPriority),
		DefaultPreemptCap:       apn.ARPPreemptionCapability,
		DefaultPreemptVuln:      apn.ARPPreemptionVulnerability,
	}

	rules, err := resolveChargingRules(ctx, store, apn.ChargingRuleList)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		flows := make([]PolicyFlow, 0)
		if rule.TFTGroupID != nil {
			tfts, err := store.GetTFTsByGroupID(ctx, *rule.TFTGroupID)
			if err != nil {
				return nil, err
			}
			for _, tft := range tfts {
				flows = append(flows, PolicyFlow{
					Description: tft.TFTString,
					Direction:   tft.Direction,
				})
			}
		}
		precedence := 100
		if rule.Precedence != nil {
			precedence = *rule.Precedence
		}
		policy.Rules = append(policy.Rules, PolicyRule{
			ID:                rule.RuleName,
			Precedence:        precedence,
			QosReference:      "qos-" + rule.RuleName,
			ChargingReference: chargingReference(rule.RuleName, rule.RatingGroup),
			Flows:             flows,
			RatingGroup:       rule.RatingGroup,
			MaxBRDownlink:     sbi.FormatBitRate(rule.MBRDown),
			MaxBRUplink:       sbi.FormatBitRate(rule.MBRUp),
			GuaranteedBRDL:    sbi.FormatBitRate(rule.GBRDown),
			GuaranteedBRUL:    sbi.FormatBitRate(rule.GBRUp),
			FiveQI:            normalize5QI(rule.QCI),
			ARP:               normalizeARP(rule.ARPPriority),
			PreemptCap:        rule.ARPPreemptionCapability,
			PreemptVuln:       rule.ARPPreemptionVulnerability,
		})
	}

	return policy, nil
}

func resolveChargingRules(ctx context.Context, store repository.Repository, rawIDs *string) ([]models.ChargingRule, error) {
	if rawIDs == nil || *rawIDs == "" {
		return nil, nil
	}
	var ids []int
	for _, s := range splitTrim(*rawIDs) {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return store.GetChargingRulesByIDs(ctx, ids)
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalize5QI(v int) int {
	if v > 0 {
		return v
	}
	return 9
}

func normalizeARP(v int) int {
	if v > 0 {
		return v
	}
	return 4
}

func normalizeSUPI(s string) string {
	return strings.TrimPrefix(s, "imsi-")
}

func normalizeDNN(s string) string {
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

func chargingReference(ruleName string, ratingGroup *int) string {
	if ratingGroup == nil {
		return ""
	}
	return "chg-" + ruleName
}
