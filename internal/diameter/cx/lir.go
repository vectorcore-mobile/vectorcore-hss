package cx

import (
	"context"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/diameter/avputil"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

func (h *Handlers) LIR(conn diam.Conn, msg *diam.Message) (*diam.Message, error) {
	var lir LIR
	if err := msg.Unmarshal(&lir); err != nil {
		h.log.Error("cx: LIR unmarshal failed", zap.Error(err))
		return avputil.ConstructFailureAnswer(msg, "", h.originHost, h.originRealm, diam.UnableToComply), err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	identity := normalizeMSISDN(string(lir.PublicIdentity))

	// Try MSISDN first, then IMSI.
	sub, err := h.store.GetIMSSubscriberByMSISDN(ctx, identity)
	if err == repository.ErrNotFound {
		sub, err = h.store.GetIMSSubscriberByIMSI(ctx, identity)
	}
	if err == repository.ErrNotFound {
		h.log.Warn("cx: LIR unknown identity", zap.String("identity", identity))
		return avputil.ConstructFailureAnswer(msg, lir.SessionID, h.originHost, h.originRealm, DiameterErrorUserUnknown), nil
	}
	if err != nil {
		return avputil.ConstructFailureAnswer(msg, lir.SessionID, h.originHost, h.originRealm, diam.UnableToComply), err
	}

	h.log.Debug("cx: LIR success", zap.String("identity", identity))
	ans := buildCxAnswer(msg, lir.SessionID, h.originHost, h.originRealm)

	if sub.SCSCF != nil {
		ans.NewAVP(avpServerName, avp.Mbit|avp.Vbit, Vendor3GPP, datatype.UTF8String(*sub.SCSCF))
	} else {
		ans.NewAVP(avpServerCapabilities, avp.Mbit|avp.Vbit, Vendor3GPP, &diam.GroupedAVP{AVP: []*diam.AVP{}})
	}
	ans.NewAVP(avp.ResultCode, avp.Mbit, 0, datatype.Unsigned32(diam.Success))
	return ans, nil
}
