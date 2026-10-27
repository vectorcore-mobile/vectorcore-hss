package crypto

import (
	"testing"

	milenage "github.com/emakeev/milenage"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

// simAUTS builds the RAND || AUTS a USIM sends on synch failure for sqnMS:
// AUTS = (SQN_MS XOR AK*) || MAC-S, where MAC-S = f1*(SQN_MS, RAND, AMF 0000).
func simAUTS(t *testing.T, sqnMS uint64) []byte {
	t.Helper()
	ki := mustDecodeHex(t, ts1K)
	opc := mustDecodeHex(t, ts1OPc)
	rand := mustDecodeHex(t, ts1RAND)
	sqnB := []byte{byte(sqnMS >> 40), byte(sqnMS >> 32), byte(sqnMS >> 24), byte(sqnMS >> 16), byte(sqnMS >> 8), byte(sqnMS)}
	akStar, err := milenage.F5Star(ki, rand, opc)
	if err != nil {
		t.Fatal(err)
	}
	_, macS, err := milenage.F1(ki, sqnB, rand, opc, []byte{0x00, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	out := append([]byte{}, rand...)
	for i := range sqnB {
		out = append(out, sqnB[i]^akStar[i])
	}
	return append(out, macS...)
}

// TestResyncSQNFull checks both the library and custom-profile paths recover
// SQN_MS from a genuine AUTS (even when the AUC's AMF is not 0000) and reject
// one whose MAC-S was tampered with.
func TestResyncSQNFull(t *testing.T) {
	const sqnMS = uint64(0x0000000012e0)
	auc := &models.AUC{Ki: ts1K, OPc: ts1OPc, AMF: "8000"}
	std := &models.AlgorithmProfile{
		C1: "00000000000000000000000000000000",
		C2: "00000000000000000000000000000001",
		C3: "00000000000000000000000000000002",
		C4: "00000000000000000000000000000004",
		C5: "00000000000000000000000000000008",
		R1: 64, R2: 0, R3: 32, R4: 64, R5: 96,
	}
	for _, c := range []struct {
		name    string
		profile *models.AlgorithmProfile
	}{
		{"library", nil},
		{"custom profile", std},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := simAUTS(t, sqnMS)
			sqn, err := ResyncSQNFull(auc, c.profile, info)
			if err != nil {
				t.Fatalf("genuine AUTS rejected: %v", err)
			}
			if uint64(sqn) != sqnMS {
				t.Errorf("SQN_MS: got %#x, want %#x", sqn, sqnMS)
			}

			info[len(info)-1] ^= 0x01
			if _, err := ResyncSQNFull(auc, c.profile, info); err == nil {
				t.Error("AUTS with bad MAC-S accepted")
			}
		})
	}
}
