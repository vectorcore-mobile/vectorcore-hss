package rx

import (
	"strings"
	"testing"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/config"
)

func TestApplyTFTHandlingUsesPCRFModeForRelayedIMSFlows(t *testing.T) {
	h := NewHandlers(&config.Config{
		PCRF: config.PCRFConfig{TFTHandling: "flip-permit-in"},
	}, nil, zap.NewNop(), nil)

	got, rewritten := h.applyTFTHandling("permit in 17 from 1.1.1.1 to 2.2.2.2")
	if !rewritten {
		t.Fatal("expected IMS flow to be rewritten")
	}

	want := "permit out 17 from 2.2.2.2 to 1.1.1.1"
	if got != want {
		t.Fatalf("unexpected TFT rewrite: got %q want %q", got, want)
	}
}

func TestBuildRxFlowInformationPreservesOriginalDirectionWithFlipPermitIn(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		fd         string
		wantPrefix string
		wantDir    uint32
	}{
		{
			name:       "downlink permit out",
			mode:       "flip-permit-in",
			fd:         "permit out 17 from 10.46.0.64 49120 to 10.46.0.61 1240",
			wantPrefix: "permit out",
			wantDir:    flowDirectionDownlink,
		},
		{
			name:       "uplink permit in rewritten to permit out",
			mode:       "flip-permit-in",
			fd:         "permit in 17 from 10.46.0.61 1240 to 10.46.0.64 49120",
			wantPrefix: "permit out",
			wantDir:    flowDirectionUplink,
		},
		{
			name:       "uplink permit in unchanged in normal mode",
			fd:         "permit in 17 from 10.46.0.61 1240 to 10.46.0.64 49120",
			wantPrefix: "permit in",
			wantDir:    flowDirectionUplink,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandlers(&config.Config{
				PCRF: config.PCRFConfig{TFTHandling: tt.mode},
			}, nil, zap.NewNop(), nil)

			desc, dir := rxFlowInformationValues(t, h.buildRxFlowInformation(tt.fd))

			if !strings.HasPrefix(desc, tt.wantPrefix) {
				t.Fatalf("unexpected Flow-Description: got %q want prefix %q", desc, tt.wantPrefix)
			}
			if dir != tt.wantDir {
				t.Fatalf("unexpected Flow-Direction: got %d want %d", dir, tt.wantDir)
			}
		})
	}
}

func TestDetectFlowDirectionFromRxFlowDescription(t *testing.T) {
	tests := []struct {
		fd      string
		wantDir uint32
	}{
		{fd: "permit out ip from any to any", wantDir: flowDirectionDownlink},
		{fd: " permit in ip from any to any", wantDir: flowDirectionUplink},
		{fd: "permit ip from any to any", wantDir: flowDirectionBidirectional},
	}

	for _, tt := range tests {
		if got := detectFlowDirectionFromRxFlowDescription(tt.fd); got != tt.wantDir {
			t.Fatalf("detectFlowDirectionFromRxFlowDescription(%q) = %d, want %d", tt.fd, got, tt.wantDir)
		}
	}
}

func rxFlowInformationValues(t *testing.T, flowAVP *diam.AVP) (string, uint32) {
	t.Helper()

	flowGroup, ok := flowAVP.Data.(*diam.GroupedAVP)
	if !ok {
		t.Fatalf("expected flow information grouped AVP, got %T", flowAVP.Data)
	}

	var desc string
	var dir uint32
	for _, child := range flowGroup.AVP {
		switch child.Code {
		case avpFlowDescription:
			value, ok := child.Data.(datatype.IPFilterRule)
			if !ok {
				t.Fatalf("expected IPFilterRule, got %T", child.Data)
			}
			desc = string(value)
		case avpGxFlowDirection:
			value, ok := child.Data.(datatype.Enumerated)
			if !ok {
				t.Fatalf("expected Enumerated, got %T", child.Data)
			}
			dir = uint32(value)
		}
	}

	if desc == "" {
		t.Fatal("missing Flow-Description")
	}
	if dir == 0 {
		t.Fatal("missing Flow-Direction")
	}

	return desc, dir
}

// Field capture: every Rx session produced the Gx rule "rx-pcscf.ims.mnc099-1"
// because the name used only the first 16 characters of the Session-Id. The
// IMS registration AAR installed it (QCI 5), the first call's AAR re-used the
// name so the P-GW modified that bearer to QCI 1 (the UE rejected, ESM #44),
// and the call's STR then deleted the signalling bearer.
func TestRxChargingRuleNameUniquePerRxSession(t *testing.T) {
	const (
		registration = "pcscf.ims.mnc099.mcc246.3gppnetwork.org;3943309833;37"
		call1        = "pcscf.ims.mnc099.mcc246.3gppnetwork.org;3943309833;39"
		otherUECall1 = "pcscf.ims.mnc099.mcc246.3gppnetwork.org;3943309833;40"
		call2        = "pcscf.ims.mnc099.mcc246.3gppnetwork.org;3943309833;41"
	)
	names := map[string]string{}
	for _, sid := range []string{registration, call1, otherUECall1, call2} {
		name := rxChargingRuleName(sid, 1)
		if prev, dup := names[name]; dup {
			t.Fatalf("Rx sessions %q and %q share Gx rule name %q", prev, sid, name)
		}
		names[name] = sid
		if name == "rx-pcscf.ims.mnc099-1" {
			t.Fatalf("rule name %q still derived from the shared Session-Id prefix", name)
		}
	}

	// A re-AAR on the same Rx session must update its own rule, not add one.
	if a, b := rxChargingRuleName(call1, 1), rxChargingRuleName(call1, 1); a != b {
		t.Fatalf("same Rx session and component gave different names %q and %q", a, b)
	}
	// Media components of one session need their own rules.
	if a, b := rxChargingRuleName(call1, 1), rxChargingRuleName(call1, 2); a == b {
		t.Fatalf("media components 1 and 2 share rule name %q", a)
	}
	if got := rxChargingRuleName(call1, 1); !strings.HasPrefix(got, "rx-") || !strings.HasSuffix(got, "-1") {
		t.Fatalf("rule name %q, want rx-<hash>-<media component>", got)
	}
}
