package main

// Request bodies, built the way the docker_open5gs Kamailio P-CSCF builds
// them with WITH_N5 (see internal/pcf/testdata/kamailio/README.md).

import (
	"encoding/json"
	"fmt"
)

const afAppID = `+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`

// party is one end of a call: the IMS address and RTP port (RTCP is +1).
type party struct {
	IP   string
	Port int
}

type ascParams struct {
	UE            string // UE IMS PDU session address (ueIpv4)
	DNN           string
	SUPI          string // sent on REGISTER
	GPSI          string // sent on calls; on MT Kamailio sends the caller's MSISDN
	NotifURI      string
	PCSCF         string // P-CSCF address, REGISTER flows
	UEPort        int    // UE RTP port
	Peer          party  // remote RTP endpoint
	BandwidthKbps int
	Video         bool
}

func evSubsc() map[string]any {
	return map[string]any{"events": []map[string]string{
		{"event": "QOS_NOTIF", "notifMethod": "PERIODIC"},
		{"event": "ANI_REPORT", "notifMethod": "ONE_TIME"},
	}}
}

// registerBody is N5_REG_PCF_POLICY_AUTH: one CONTROL component for SIP.
func registerBody(p ascParams) []byte {
	comp := map[string]any{
		"medCompN": 0,
		"medType":  "CONTROL",
		"medSubComps": map[string]any{"0": map[string]any{
			"fNum": 0,
			"fDescs": []string{
				fmt.Sprintf("permit out ip from %s 5060 to %s 5060", p.PCSCF, p.UE),
				fmt.Sprintf("permit in ip from %s 5060 to %s 5060", p.UE, p.PCSCF),
			},
			"fStatus":   "ENABLED",
			"flowUsage": "AF_SIGNALLING",
		}},
	}
	asc := map[string]any{
		"afAppId":       afAppID,
		"dnn":           p.DNN,
		"medComponents": map[string]any{"0": comp},
		"evSubsc":       evSubsc(),
		"notifUri":      p.NotifURI,
		"sponStatus":    "SPONSOR_DISABLED",
		"supi":          p.SUPI,
		"suppFeat":      "2",
		"ueIpv4":        p.UE,
	}
	return mustJSON(map[string]any{"ascReqData": asc})
}

// mediaComponent is one SDP m= line: RTP and RTCP flows in both directions.
func mediaComponent(n int, medType, ue string, uePort int, peer party, bwKbps int) map[string]any {
	bw := fmt.Sprintf("%d Kbps", bwKbps)
	return map[string]any{
		"medCompN": n,
		"medType":  medType,
		"codecs":   []string{"downlink\n0\n", "uplink\n0\n"},
		"medSubComps": map[string]any{"0": map[string]any{
			"fNum": n,
			"fDescs": []string{
				fmt.Sprintf("permit out 17 from %s %d to %s %d", peer.IP, peer.Port, ue, uePort),
				fmt.Sprintf("permit in 17 from %s %d to %s %d", ue, uePort, peer.IP, peer.Port),
				fmt.Sprintf("permit out 17 from %s %d to %s %d", peer.IP, peer.Port+1, ue, uePort+1),
				fmt.Sprintf("permit in 17 from %s %d to %s %d", ue, uePort+1, peer.IP, peer.Port+1),
			},
			"fStatus":   "ENABLED",
			"marBwDl":   bw,
			"marBwUl":   bw,
			"flowUsage": "NO_INFO",
		}},
	}
}

// callBody is MO_/MT_N5_CALL_PCF_POLICY_AUTH: an audio component, plus a
// video one on ports +2/+3 when p.Video is set. The same body is PATCHed on
// a re-INVITE/UPDATE.
func callBody(p ascParams) []byte {
	comps := map[string]any{"0": mediaComponent(1, "AUDIO", p.UE, p.UEPort, p.Peer, p.BandwidthKbps)}
	if p.Video {
		vpeer := party{IP: p.Peer.IP, Port: p.Peer.Port + 2}
		comps["1"] = mediaComponent(2, "VIDEO", p.UE, p.UEPort+2, vpeer, p.BandwidthKbps)
	}
	asc := map[string]any{
		"afAppId":       afAppID,
		"dnn":           p.DNN,
		"medComponents": comps,
		"evSubsc":       evSubsc(),
		"notifUri":      p.NotifURI,
		"sponStatus":    "SPONSOR_DISABLED",
		"gpsi":          p.GPSI,
		"suppFeat":      "2",
		"ueIpv4":        p.UE,
	}
	return mustJSON(map[string]any{"ascReqData": asc})
}

// smPolicyBody plays the SMF creating the IMS PDU session's SM policy.
func smPolicyBody(supi, dnn, ueIP, notifURI string) []byte {
	return mustJSON(map[string]any{
		"supi":            supi,
		"dnn":             dnn,
		"pduSessionId":    5,
		"pduSessionType":  "IPV4",
		"ipv4Address":     ueIP,
		"notificationUri": notifURI,
		"sliceInfo":       map[string]any{"sst": 1},
		"suppFeat":        "0",
	})
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return b
}
