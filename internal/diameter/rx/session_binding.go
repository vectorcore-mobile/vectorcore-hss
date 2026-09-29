package rx

import (
	"context"
	"net"

	"github.com/svinson1121/vectorcore-hss/internal/models"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// rxBindingStore is the subset of the repository the Rx→Gx session binding
// needs.
type rxBindingStore interface {
	GetServingAPNByUEIP(ctx context.Context, ueIP string) (*models.ServingAPN, error)
	GetServingAPNByIdentity(ctx context.Context, identity string) (*models.ServingAPN, error)
	GetSubscriberByIMSI(ctx context.Context, imsi string) (*models.Subscriber, error)
	GetSubscriberByMSISDN(ctx context.Context, msisdn string) (*models.Subscriber, error)
}

const (
	rxBindingUEIP     = "ue-ip"
	rxBindingIdentity = "identity"
)

// bindRxToGxSession finds the Gx session an AAR's rules belong to. Per TS
// 29.213 §4 the binding key is the UE IP address: a subscriber with both an
// internet and an IMS PDN has two Gx sessions, and only the one owning the
// AAR's Framed-IP-Address may carry the media bearers. The identity-only
// lookup, which returns the subscriber's oldest session, is kept as a
// fallback for an AAR without a usable UE address.
func bindRxToGxSession(ctx context.Context, store rxBindingStore, identity string, framedIP []byte) (*models.ServingAPN, string, error) {
	if len(framedIP) == net.IPv4len {
		rec, err := store.GetServingAPNByUEIP(ctx, net.IP(framedIP).String())
		if err == nil && rec.PCRFSessionID != nil && rec.ServingPGWPeer != nil && belongsToIdentity(ctx, store, rec, identity) {
			return rec, rxBindingUEIP, nil
		}
		if err != nil && err != repository.ErrNotFound {
			return nil, "", err
		}
	}
	rec, err := store.GetServingAPNByIdentity(ctx, identity)
	return rec, rxBindingIdentity, err
}

// belongsToIdentity guards against a stale serving_apn row for a reused UE
// IP: a match owned by a different subscriber is rejected. When the identity
// does not resolve to a subscriber (e.g. an unrecognised SIP-URI local part),
// the UE IP match is trusted.
func belongsToIdentity(ctx context.Context, store rxBindingStore, rec *models.ServingAPN, identity string) bool {
	if identity == "" {
		return true
	}
	sub, err := store.GetSubscriberByIMSI(ctx, identity)
	if err != nil {
		sub, err = store.GetSubscriberByMSISDN(ctx, identity)
	}
	if err != nil {
		return true
	}
	return sub.SubscriberID == rec.SubscriberID
}
