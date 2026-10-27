package crypto

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	milenage "github.com/emakeev/milenage"

	"github.com/svinson1121/vectorcore-hss/internal/metrics"
	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// LoadProfile fetches the AlgorithmProfile for an AUC if one is assigned.
// Returns nil, nil when no profile is set — callers use standard Milenage in that case.
func LoadProfile(ctx context.Context, store repository.Repository, profileID *int64) (*models.AlgorithmProfile, error) {
	if profileID == nil {
		return nil, nil
	}
	p, err := store.GetAlgorithmProfile(ctx, *profileID)
	if err == repository.ErrNotFound {
		// Profile was deleted — fall back to standard Milenage.
		return nil, nil
	}
	return p, err
}

type EUTRANVector struct {
	RAND  []byte
	XRES  []byte
	AUTN  []byte
	KASME []byte
}

func GenerateEUTRANVectors(auc *models.AUC, profile *models.AlgorithmProfile, plmn []byte, n uint32, store repository.Repository, ctx context.Context) ([]EUTRANVector, error) {
	ki, err := hex.DecodeString(auc.Ki)
	if err != nil {
		return nil, fmt.Errorf("decode Ki: %w", err)
	}
	opc, err := hex.DecodeString(auc.OPc)
	if err != nil {
		return nil, fmt.Errorf("decode OPc: %w", err)
	}
	amf, err := hex.DecodeString(auc.AMF)
	if err != nil {
		return nil, fmt.Errorf("decode AMF: %w", err)
	}
	if len(plmn) != 3 {
		return nil, fmt.Errorf("PLMN must be 3 bytes, got %d", len(plmn))
	}

	before, err := store.AtomicGetAndIncrementSQN(ctx, auc.AUCID, int64(n)*32)
	if err != nil {
		return nil, fmt.Errorf("increment SQN: %w", err)
	}

	start := time.Now()
	vectors := make([]EUTRANVector, n)

	if profile != nil {
		// Custom profile path — use our own Milenage implementation.
		pc, err := decodeProfile(profile)
		if err != nil {
			return nil, err
		}
		for i := uint32(0); i < n; i++ {
			sqn := uint64(before.SQN + int64(i)*32)
			v, err := GenerateEUTRANVectorCustom(ki, opc, amf, sqn, plmn, pc)
			if err != nil {
				return nil, fmt.Errorf("generate vector %d: %w", i, err)
			}
			vectors[i] = v
		}
	} else {
		// Standard path — use the emakeev/milenage library.
		cipher, err := milenage.NewCipher(amf)
		if err != nil {
			return nil, fmt.Errorf("new cipher: %w", err)
		}
		for i := uint32(0); i < n; i++ {
			sqn := uint64(before.SQN + int64(i)*32)
			v, err := cipher.GenerateEutranVector(ki, opc, sqn, plmn)
			if err != nil {
				return nil, fmt.Errorf("generate vector %d: %w", i, err)
			}
			vectors[i] = EUTRANVector{RAND: v.Rand[:], XRES: v.Xres[:], AUTN: v.Autn[:], KASME: v.Kasme[:]}
		}
	}

	metrics.VectorGenerationDuration.WithLabelValues("eutran").Observe(time.Since(start).Seconds())
	return vectors, nil
}

// EAPAKAVector holds the quintuplet used for EAP-AKA authentication (SWx/non-3GPP access).
type EAPAKAVector struct {
	RAND []byte
	XRES []byte
	AUTN []byte
	CK   []byte // Confidentiality Key
	IK   []byte // Integrity Key
}

// DeriveEAPAKAPrimeKeys derives CK' and IK' for EAP-AKA' per TS 33.402 Annex A.4.
// The SQN XOR AK input is the first six octets of AUTN.
func DeriveEAPAKAPrimeKeys(ck, ik []byte, anid string, sqnXorAK []byte) ([]byte, []byte, error) {
	if len(ck) != 16 || len(ik) != 16 {
		return nil, nil, fmt.Errorf("EAP-AKA' derivation requires 16-byte CK and IK")
	}
	if anid == "" {
		return nil, nil, fmt.Errorf("EAP-AKA' derivation requires ANID")
	}
	if len(sqnXorAK) != 6 {
		return nil, nil, fmt.Errorf("EAP-AKA' derivation requires 6-byte SQN XOR AK")
	}

	key := append(append([]byte{}, ck...), ik...)
	s := []byte{0x20}
	s = appendKDFParam(s, []byte(anid))
	s = appendKDFParam(s, sqnXorAK)

	mac := hmac.New(sha256.New, key)
	mac.Write(s)
	out := mac.Sum(nil)
	return out[:16], out[16:32], nil
}

func appendKDFParam(dst, p []byte) []byte {
	dst = append(dst, p...)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(p)))
	return append(dst, l[:]...)
}

// GenerateEAPAKAVector generates a single EAP-AKA quintuplet from the subscriber's
// AUC credentials. The SQN is atomically incremented in the database.
func GenerateEAPAKAVector(auc *models.AUC, profile *models.AlgorithmProfile, store repository.Repository, ctx context.Context) (*EAPAKAVector, error) {
	ki, err := hex.DecodeString(auc.Ki)
	if err != nil {
		return nil, fmt.Errorf("decode Ki: %w", err)
	}
	opc, err := hex.DecodeString(auc.OPc)
	if err != nil {
		return nil, fmt.Errorf("decode OPc: %w", err)
	}
	amf, err := hex.DecodeString(auc.AMF)
	if err != nil {
		return nil, fmt.Errorf("decode AMF: %w", err)
	}

	before, err := store.AtomicGetAndIncrementSQN(ctx, auc.AUCID, 32)
	if err != nil {
		return nil, fmt.Errorf("increment SQN: %w", err)
	}

	start := time.Now()
	sqn := uint64(before.SQN)

	var vec EAPAKAVector
	if profile != nil {
		pc, err := decodeProfile(profile)
		if err != nil {
			return nil, err
		}
		v, err := GenerateEAPAKAVectorCustom(ki, opc, amf, sqn, pc)
		if err != nil {
			return nil, fmt.Errorf("generate EAP-AKA vector: %w", err)
		}
		vec = v
	} else {
		cipher, err := milenage.NewCipher(amf)
		if err != nil {
			return nil, fmt.Errorf("new cipher: %w", err)
		}
		v, err := cipher.GenerateSIPAuthVector(ki, opc, sqn)
		if err != nil {
			return nil, fmt.Errorf("generate SIP auth vector: %w", err)
		}
		vec = EAPAKAVector{
			RAND: v.Rand[:],
			XRES: v.Xres[:],
			AUTN: v.Autn[:],
			CK:   v.ConfidentialityKey[:],
			IK:   v.IntegrityKey[:],
		}
	}

	metrics.VectorGenerationDuration.WithLabelValues("eap_aka").Observe(time.Since(start).Seconds())
	return &vec, nil
}

func ResyncSQNFull(auc *models.AUC, profile *models.AlgorithmProfile, resyncInfo []byte) (int64, error) {
	if len(resyncInfo) != 30 {
		return 0, fmt.Errorf("ResyncInfo must be 30 bytes, got %d", len(resyncInfo))
	}
	ki, err := hex.DecodeString(auc.Ki)
	if err != nil {
		return 0, err
	}
	opc, err := hex.DecodeString(auc.OPc)
	if err != nil {
		return 0, err
	}

	if profile != nil {
		pc, err := decodeProfile(profile)
		if err != nil {
			return 0, err
		}
		return ResyncCustom(ki, opc, resyncInfo, pc)
	}

	// f1* takes a dummy AMF of 0000 (TS 33.102 6.3.3), not the subscriber's.
	cipher, err := milenage.NewCipher([]byte{0x00, 0x00})
	if err != nil {
		return 0, err
	}
	rand := resyncInfo[:16]
	auts := resyncInfo[16:]
	sqn, macS, err := cipher.GenerateResync(auts, ki, opc, rand)
	if err != nil {
		return 0, err
	}
	if !hmac.Equal(macS[:], auts[6:14]) {
		return 0, fmt.Errorf("milenage resync: MAC-S verification failed")
	}
	return int64(sqn), nil
}
