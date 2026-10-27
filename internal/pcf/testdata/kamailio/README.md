# Kamailio P-CSCF N5 request bodies

These are the Npcf_PolicyAuthorization bodies the docker_open5gs Kamailio
P-CSCF builds in its script when `WITH_N5` is defined
(https://github.com/herlesupreeth/docker_open5gs, commit ff41a30,
`pcscf/route/register.cfg` `N5_REG_PCF_POLICY_AUTH`, `pcscf/route/mo.cfg`
`MO_N5_CALL_PCF_POLICY_AUTH`, `pcscf/route/mt.cfg` `MT_N5_CALL_PCF_POLICY_AUTH`),
with the script variables filled in:

| Value | Meaning |
|---|---|
| 10.46.0.26 | UE A (caller) — IMS PDU session address |
| 10.46.0.27 | UE B (callee) |
| 10.46.0.1  | P-CSCF; N5 listener (notifUri) on :7777 |
| 50000/50001, 40000/40001 | caller / callee RTP/RTCP ports |
| `38 Kbps`  | from `b=AS:38`; Kamailio sends `0 Kbps` when the SDP has none |

Kamailio POSTs `register.json`, `mo_invite*.json` and `mt_invite.json` to
`/npcf-policyauthorization/v1/app-sessions` and expects 201 with a Location
header. On a re-INVITE/UPDATE it PATCHes the same body (`mo_reinvite.json`)
to `/app-sessions/{id}` and expects 200. On BYE/CANCEL/error it POSTs an
empty body to `/app-sessions/{id}/delete`. Any other answer to the REGISTER
body makes it reply 412 to the UE's REGISTER; any other answer to a call body
makes it tear the call down.

Note: on MT calls Kamailio sets `gpsi` to the caller's MSISDN, so the PCF
must bind by `ueIpv4`, not by identity.
