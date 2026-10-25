# VectorCore HSS  -- OAM REST API Reference

The OAM (Operations, Administration, and Maintenance) API provides full CRUD management of all HSS subscriber data. It is a JSON REST API built on **Huma v2** (OpenAPI 3.1) with interactive docs available at `/api/v1/docs`.

---

## Configuration

```yaml
# config.yaml
hss:
  OriginHost: hss01.epc.mnc435.mcc311.3gppnetwork.org
  OriginRealm: epc.mnc435.mcc311.3gppnetwork.org
  MCC: "311"   # Mobile Country Code  -- used for IFC profile template substitution
  MNC: "435"   # Mobile Network Code  -- used for IFC profile template substitution

gsup:
  enabled: false          # set true to start the GSUP/HLR listener
  bind_address: "::"      # :: = all interfaces (IPv4+IPv6)
  bind_port: 4222         # standard Osmocom GSUP port

5gc:
  sbi:
    bind_address: "::"
    bind_port: 7777
    # advertise_address: 10.0.2.117   # IPv4 registered with the NRF; default: bind_address if concrete, else the source IP routing to the NRF
    # tls_cert_file: /etc/hss/tls/5gc.crt
    # tls_key_file:  /etc/hss/tls/5gc.key
    oauth2_enabled: false
    oauth2_bypass: true
    client:
      mode: direct
      scp_address: ""
  udm:
    enabled: false
    mcc: "311"
    mnc: "435"
    nrf_address: ""
    nf_instance_id: ""
    suci_decryption_keys: []
  pcf:
    enabled: false
    mcc: "311"
    mnc: "435"
    nrf_address: ""
    nf_instance_id: ""

eir:
  no_match_response: 0        # Response when no EIR rule matches: 0=WHITELISTED, 1=BLACKLISTED, 2=GREYLISTED
  imsi_imei_logging: true     # write IMSI+IMEI pairs to eir_history on every S13 check
  tac_db_enabled: true        # load GSMA Type Allocation Code (IMEI device) DB into memory
                              # NOTE: TAC here = IMEI Type Allocation Code, NOT the RAN Tracking Area Code

api:
  enabled: true
  bind_address: "0.0.0.0"
  bind_port: 8080              # default
  # tls_cert_file: /etc/hss/tls/cert.pem   # enable HTTPS
  # tls_key_file:  /etc/hss/tls/key.pem
```

**Base URL (plain HTTP):** `http://<host>:8080`
**Base URL (HTTPS):** `https://<host>:8080`  -- enabled automatically when `tls_cert_file` and `tls_key_file` are set.

**Interactive API docs:** `http://<host>:8080/api/v1/docs`
**OpenAPI 3.1 spec (used by the docs page):** `http://<host>:8080/api/v1/openapi.yaml`
**OpenAPI 3.1 spec (JSON form):** `http://<host>:8080/api/v1/openapi.json`

Authentication is optional and disabled by default  -- restrict access at the network/firewall level when not using API keys.

---

## GSUP / HLR Interface

VectorCore HSS includes an Osmocom **GSUP** (Generic Subscriber Update Protocol) server for circuit-switched 2G/3G integration with **OsmoMSC** and **OsmoSGSN**.

### Use cases
- **SGs SMS** -- OsmoMSC authenticates LTE subscribers via GSUP for SMS delivery over the SGs interface
- **CSFB voice** -- Circuit Switched Fallback to 2G RAN; BSS/MSC authenticates the subscriber using 2G GSM triplets derived from Milenage

### Protocol
- Transport: TCP (default port **4222**), framed with the Osmocom **IPA** protocol
- Application: **GSUP** TLV messages over IPA extension 0x05

### Messages handled

| Message | Direction | Description |
|---------|-----------|-------------|
| SendAuthInfo (0x08) | MSC/SGSN -> HSS | Request authentication vectors. HSS returns both 2G triplet (RAND/SRES/KC) and 3G quintuplet (RAND/XRES/CK/IK/AUTN) derived from Milenage. |
| UpdateLocation (0x04) | MSC/SGSN -> HSS | Location update. HSS stores serving VLR, responds with ULA then pushes InsertSubscriberData (MSISDN). |
| PurgeMS (0x1C) | MSC/SGSN -> HSS | Subscriber detached. HSS clears serving VLR. |

### 2G key derivation
For CSFB to 2G RAN, GSM auth triplets are derived from Milenage per **3GPP TS 55.205**:
- `SRES` = `XRES[0:4]`
- `KC` = `CK[0:8] XOR CK[8:16] XOR IK[0:8] XOR IK[8:16]`

No COMP128 support -- all subscribers are assumed to have Milenage SIMs (required for LTE).

### Enable in config.yaml
```yaml
gsup:
  enabled: true
  bind_address: "::"
  bind_port: 4222
```

---

## S6c / SMS Routing Interface

VectorCore HSS implements the **S6c** Diameter interface (3GPP TS 29.338) for SMS routing. This interface allows SMS-SC peers to query subscriber routing information and report SMS delivery outcomes. The HSS also sends unsolicited **Alert-Service-Centre** notifications when a subscriber becomes reachable after a failed delivery.

### Use cases
- **MT SMS routing** — SMS-SC queries the HSS for the serving MME address to deliver a mobile-terminated SMS
- **Message Waiting Data** — SMS-SC reports a failed delivery; the HSS stores a pending notification record
- **Subscriber reachability** — on successful LTE attach (ULR), the HSS automatically sends ALSC to any SMS-SC with a pending MWD record for that subscriber

### Protocol
- Transport: TCP/SCTP (same Diameter listener as S6a, Gx, Cx)
- Application ID: **16777312** (3GPP S6c)
- Reference: **3GPP TS 29.338**

### Commands

| Command | Code | Direction | Description |
|---------|------|-----------|-------------|
| **SRI-SM** (Send-Routing-Info-for-SM) | 8388647 | SMS-SC → HSS | Query subscriber serving-node info for MT SMS delivery |
| **ALSC** (Alert-Service-Centre) | 8388648 | HSS → SMS-SC | Notify SMS-SC that a subscriber is now reachable (HSS-initiated) |
| **RSDS** (Report-SM-Delivery-Status) | 8388649 | SMS-SC → HSS | Report a delivery failure and store a Message Waiting Data record |

### SRI-SM

The SMS-SC sends an SRI-SM request with the subscriber's MSISDN (or IMSI as a fallback). The HSS:

1. Resolves the subscriber by MSISDN; falls back to IMSI if the MSISDN field is absent
2. Returns the subscriber's **serving MME** name, realm, and address if the UE is currently attached
3. Sets the **MWD-Status MNRF** (Mobile Not Reachable Flag) bit in the answer if the UE is not attached

**AVPs returned in the answer:**
- `User-Name` — IMSI
- `MSISDN` — TBCD-encoded E.164 number
- `Serving-Node` — MME Origin-Host + Origin-Realm
- `MWD-Status` — MNRF bit set when subscriber is absent

### RSDS / Message Waiting Data

The SMS-SC sends an RSDS request to report the outcome of an MT SMS delivery attempt. The HSS inspects the **SM-Delivery-Outcome** AVP (TS 29.338 §7.3.16) and acts accordingly:

| SM-Delivery-Cause | HSS action | MWD-Status returned |
|-------------------|-----------|---------------------|
| `SuccessfulTransfer` (2) | Clears any existing MWD record; no ALSC will be sent | — |
| `AbsentUser` (1) | Stores MWD record with MNRF flag | MNRF (0x02) |
| `MemoryCapacityExceeded` (0) | Stores MWD record with MCEF flag | MCEF (0x04) |
| AVP absent | Treated as AbsentUser | MNRF (0x02) |

**MWD record fields:**

| Field | Description |
|-------|-------------|
| `imsi` | Subscriber IMSI |
| `sc_address` | SMS-SC E.164 address |
| `sc_origin_host` | SMS-SC Diameter Origin-Host (used to route ALSC back) |
| `sc_origin_realm` | SMS-SC Diameter Origin-Realm (used as Destination-Realm in ALSC) |
| `sm_rp_mti` | 0 = SM-Deliver, 1 = SMS-Submit-Report |
| `mwd_status_flags` | Status flags stored at record creation (MNRF or MCEF) |

MWD records are unique per `(imsi, sc_address)`. A subsequent failure from the same SMS-SC overwrites the existing record. The record is deleted automatically after ALSC is acknowledged by the SMS-SC.

### ALSC / Alert-Service-Centre (HSS-initiated)

After a successful ULR (LTE attach), the S6a handler calls a registered hook in the S6c layer. The S6c layer then:

1. Queries pending MWD records for the attaching subscriber's IMSI
2. Looks up the active Diameter connection for each SMS-SC's `origin_host`
3. Sends an ALSC request on that connection, using the stored `sc_origin_realm` as `Destination-Realm`
4. Waits for the SMS-SC's **Alert-Service-Centre-Answer (ASA)**
5. Deletes the MWD record only when the ASA returns `Result-Code 2001` (DIAMETER_SUCCESS)
6. Retains the MWD record on ASA failure or if the SMS-SC is not currently connected (retried on next attach)

No operator action is required — the ALSC flow is fully automatic.

### MSISDN encoding

MSISDN values on the S6c interface are **TBCD-encoded** (Telephone Binary Coded Decimal) per TS 29.338 §7.3. The HSS encodes and decodes transparently; subscriber provisioning uses plain E.164 strings (e.g. `"13135551234"`).

### Configuration

No separate config section is required. S6c is automatically active when the Diameter server starts. SMS-SC peers connect on the same Diameter port as MME peers.

---

## 5G UDR/UDM/PCF Interface

VectorCore HSS includes 5G **UDM, UDR, and PCF** services on the SBI (Service Based Interface). UDM and PCF can share a single HTTP/2 listener while still registering as separate NF types in NRF. VectorCore acts as both the UDM (application logic) and UDR (data repository) in a single process — there is no separate UDR service; it goes directly to the same PostgreSQL database as the 4G HSS.

### Use cases
- **5G-AKA authentication** — AUSF calls `nudm-ueau` to get auth vectors for 5G SA registration
- **Subscription data** — AMF/SMF fetch AMBR, NSSAI, and DNN configs via `nudm-sdm`
- **UE context management** — AMF/SMF register/deregister serving nodes via `nudm-uecm`
- **Policy control** — AMF and SMF call `npcf-am-policy-control` and `npcf-smpolicycontrol` for AM and session policy decisions

### Protocol
- Transport: **HTTP/2** (h2c cleartext for lab, TLS+h2 for production)
- Default port: **7777**
- Shared listener: UDM and PCF can run on the same `5gc.sbi.bind_address:bind_port`
- URL prefix: `/nudm-{ueau,sdm,uecm}/v{1,2}/{supi}/...`
- SUPI format: `imsi-{15digits}` or SUCI null-scheme `suci-0-{MCC}-{MNC}-0-0-0-{MSIN}`

### Interfaces served

| 3GPP ref | SBI name | URL Prefix | Caller | Description |
|----------|---------|-----------|--------|-------------|
| N13 | nudm-ueau | `/nudm-ueau/v{1,2}/{supi}` | AUSF | Generate 5G-AKA auth vectors (RAND/AUTN/XRES*/KAUSF) |
| N8  | nudm-sdm  | `/nudm-sdm/v{1,2}/{supi}`  | AMF | AM-data, NSSAI, SDM subscriptions |
| N10 | nudm-sdm  | `/nudm-sdm/v{1,2}/{supi}`  | SMF | SM-data, SMF-select-data |
| N8/N10 | nudm-uecm | `/nudm-uecm/v1/{supi}` | AMF, SMF | Register/deregister serving AMF and PDU sessions |
| N36 | nudr-dr   | `/nudr-dr/v{1,2}/policy-data/ues/{ueId}` | PCF | AM policy data, SM policy data, UE policy set, SMS management |
| N7 | npcf-am-policy-control | `/npcf-am-policy-control/v1/policies` | AMF | AM policy create/update/delete |
| N7 | npcf-smpolicycontrol | `/npcf-smpolicycontrol/v1/sm-policies` | SMF | SM policy create/update/delete |

### 5G-AKA key derivation

Auth vectors are computed on top of the subscriber's existing Milenage `Ki` and `OPc` (from the `auc` table):

| Output | Derivation | Spec |
|--------|-----------|------|
| `RAND`, `AUTN`, `CK`, `IK` | Standard Milenage | TS 35.206 |
| `KAUSF` | `HMAC-SHA-256(CK\|\|IK, 0x6A \|\| SNN \|\| len(SNN) \|\| SQN⊕AK \|\| 0x0006)` | TS 33.501 Annex A.2 |
| `XRES*` | `HMAC-SHA-256(CK\|\|IK, 0x6B \|\| SNN \|\| RAND \|\| XRES)[16:32]` | TS 33.501 Annex A.4 |

SQN increment is atomic in the database — the same `auc.sqn` column used by the 4G HSS is reused.

### Subscriber 5G fields

The following fields on the `subscriber` record are relevant to the UDM:

| Field | JSON key | Description |
|-------|----------|-------------|
| `nssai` | `nssai` | Allowed S-NSSAIs as a JSON array, e.g. `[{"sst":1},{"sst":2,"sd":"000001"}]`. Defaults to `[{"sst":1}]` (eMBB) when null. |
| `ue_ambr_dl` | `ue_ambr_dl` | UE aggregate max bit rate downlink (kbps). Returned in am-data as `subscribedUeAmbr`. |
| `ue_ambr_ul` | `ue_ambr_ul` | UE aggregate max bit rate uplink (kbps). |
| `apn_list` | `apn_list` | Comma-separated APN IDs. Each APN becomes a DNN config in sm-data. |
| `msisdn` | `msisdn` | If set, returned as a GPSI (`msisdn-{number}`) in am-data. |
| `serving_amf` | `serving_amf` | Serving AMF address (written by nudm-uecm PUT, read-only). |
| `serving_amf_instance_id` | `serving_amf_instance_id` | AMF NF instance UUID (read-only). |
| `serving_amf_timestamp` | `serving_amf_timestamp` | Timestamp of last AMF registration (read-only). |

Set `nssai` via the subscriber update endpoint:

```bash
curl -X PUT http://localhost:8080/api/v1/subscriber/1 \
  -H "Content-Type: application/json" \
  -d '{"imsi":"001010000000001","auc_id":1,"default_apn":1,"apn_list":"1","nssai":"[{\"sst\":1},{\"sst\":2,\"sd\":\"000001\"}]"}'
```

### N36 — PCF policy data endpoints

The PCF calls these during UE registration (AM) and PDU session establishment (SM):

| Method | Path | Description |
|--------|------|-------------|
| GET | `/nudr-dr/v1/policy-data/ues/{ueId}/am-data` | AM policy data: subscribedUeAmbr, rfspIndex |
| GET | `/nudr-dr/v1/policy-data/ues/{ueId}/sm-data` | SM policy data: per-DNN/slice session AMBR, QoS flows |
| GET | `/nudr-dr/v1/policy-data/ues/{ueId}/ue-policy-set` | UE policy set (URSP rules — empty set returned) |
| GET | `/nudr-dr/v1/policy-data/sms-management-data/{ueId}` | SMS subscription flags |

`{ueId}` accepts the same SUPI formats as the Nudm endpoints (`imsi-{15digits}` or SUCI null-scheme).

SM policy data supports `?dnn=<dnn>` and `?snssai={"sst":1}` query parameters for PCF filtering.

Data sources — all derived from existing tables, no new DB columns required:

| Response field | Source |
|----------------|--------|
| `subscribedUeAmbr` | `subscriber.ue_ambr_ul` / `ue_ambr_dl` (kbps → string) |
| `sessionAmbr` | `apn.apn_ambr_ul` / `apn_ambr_dl` per DNN |
| `singleNssai` | `subscriber.nssai` JSON array |
| `5qi` / `arp` | `apn.qci`, `apn.arp_priority`, `apn.arp_preemption_*` |

### NRF registration

When `nrf_address` is set VectorCore registers the enabled UDM and PCF instances with the NRF at startup using `PUT /nnrf-nfm/v1/nf-instances/{nf_instance_id}` and maintains a heartbeat `PATCH` every 60 s. The `nf_instance_id` is auto-generated (UUID v4) if left blank in config.

### OAuth2

When `oauth2_enabled: true` the UDM and PCF validate the `Authorization: Bearer <JWT>` header on inbound SBI requests. The JWKS is fetched from the NRF at startup and cached. Set `oauth2_bypass: true` to skip validation in lab deployments. If `5gc.sbi.client.mode: scp` is used, set `5gc.sbi.client.scp_address` to the absolute SCP API root.

### PDU session state (OAM read-only)

Active 5G PDU sessions registered by SMF via nudm-uecm are visible through the OAM API:

```
GET /api/v1/oam/pdu_session
GET /api/v1/oam/pdu_session/imsi/{imsi}
```

### Enable in config.yaml

```yaml
5gc:
  sbi:
    bind_address: "::"
    bind_port: 7777
    client:
      mode: direct
      scp_address: ""
    oauth2_bypass: true
  udm:
    enabled: true
    nrf_address: "http://127.0.0.5:7777"
  pcf:
    enabled: true
    nrf_address: "http://127.0.0.5:7777"
```

---

## Authentication

The API supports optional API key authentication. When enabled, every request (except `GET /metrics`) must include a valid API key.

### Configuration

```yaml
api:
  auth_enabled: true
  api_keys:
    - "your-secret-key-here"
    - "another-key-if-needed"
```

Set `auth_enabled: false` (the default) to disable authentication entirely.

### Sending an API Key

**HTTP header (preferred):**

```
X-API-Key: your-secret-key-here
```

**Query parameter (alternative):**

```
GET /subscriber?api_key=your-secret-key-here
```

If authentication is enabled and no valid key is provided, the API returns HTTP 401.

---

## Conventions

| Item | Detail |
|------|--------|
| Content-Type | `application/json` for all request bodies |
| Encoding | UTF-8 |
| `last_modified` | Set automatically on create/update (RFC 3339 UTC). Do not send in request body. |
| `{id}` | Integer primary key in the path |
| List responses | Resource-specific. Most list endpoints return JSON arrays; some, such as `/api/v1/subscriber`, return an object with metadata like `total` and `items`. |
| Optional fields | Fields with server defaults (primary keys, `last_modified`, bool/int fields with GORM defaults) may be omitted from create/update request bodies. The server applies the DB default. |

### HTTP Status Codes

| Code | Meaning |
|------|---------|
| 200 | OK  -- GET or PUT success |
| 201 | Created  -- POST success |
| 204 | No Content  -- DELETE success |
| 400 | Bad Request  -- malformed JSON or invalid path parameter |
| 401 | Unauthorized  -- API key required but missing or invalid |
| 404 | Not Found  -- record does not exist |
| 422 | Unprocessable Entity  -- request body failed schema validation |
| 500 | Internal Server Error  -- database error |

### Error Response Body

Most Huma-managed API errors follow the RFC 7807 Problem Details format:

```json
{
  "title": "Not Found",
  "status": 404,
  "detail": "not found"
}
```

API-key authentication failures currently return a simpler body:

```json
{
  "error": "unauthorized"
}
```

Validation errors (422) include a per-field breakdown:

```json
{
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "validation failed",
  "errors": [
    {
      "message": "expected required property apn to be present",
      "location": "body",
      "value": {}
    }
  ]
}
```

---

## OAM

Operations, Administration, and Maintenance endpoints. Includes system monitoring, runtime state, and read-only views of dynamic session data.

### Prometheus Metrics

```
GET /metrics
```

Returns Prometheus text format metrics. Scrape with any Prometheus-compatible collector. This endpoint is outside the versioned API prefix.

```bash
curl http://localhost:8080/metrics
```

---

### JSON Metrics

```
GET /api/v1/oam/metrics
```

Returns a structured JSON snapshot of all HSS metrics. Designed for web dashboard consumption  -- no Prometheus required.

**Key metrics tracked:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hss_diameter_requests_total` | Counter | `command`, `result` | Diameter requests by command and result |
| `hss_diameter_request_duration_seconds` | Histogram | `command` | Handler latency per command |
| `hss_db_query_duration_seconds` | Histogram | `operation`, `table` | Database query latency |
| `hss_api_requests_total` | Counter | `method`, `path`, `status` | OAM API request counts |
| `hss_api_request_duration_seconds` | Histogram | `method`, `path` | OAM API request latency |
| `hss_cache_hits_total` | Counter | `entity`, `result` | In-memory cache hit/miss by entity type |
| `hss_crypto_vector_generation_seconds` | Histogram | `type` | Milenage vector generation time (`eutran`, `eap_aka`) |
| `hss_tac_lookups_total` | Counter | `result` | TAC (IMEI device) cache lookups  -- `hit` or `miss` |
| `hss_tac_cache_size` | Gauge |  -- | Number of TAC entries currently loaded in memory |
| `hss_tac_imported_total` | Counter |  -- | Cumulative TAC records written via CSV import |
| `hss_db_pool_*` | Gauge/Counter |  -- | DB pool state and wait metrics |

**Response:**

```json
{
  "timestamp": "2026-03-16T05:14:30Z",
  "diameter": {
    "commands": [
      { "command": "AIR", "total": 120, "errors": 2, "error_rate_pct": 1.67, "mean_ms": 1.5 },
      { "command": "ULR", "total": 118, "errors": 0, "error_rate_pct": 0, "mean_ms": 1.2 }
    ]
  },
  "database": {
    "queries": [
      { "operation": "query", "table": "subscriber", "count": 220, "mean_ms": 0.42, "sum_ms": 92.4 }
    ],
    "pool": {
      "open_connections": 4,
      "in_use": 1,
      "idle": 3,
      "wait_count": 0,
      "wait_duration_ms": 0
    }
  },
  "cache": {
    "entities": [
      { "entity": "subscriber", "hits": 100, "misses": 20, "hit_rate_pct": 83.33 },
      { "entity": "auc", "hits": 98, "misses": 22, "hit_rate_pct": 81.67 }
    ]
  },
  "api": {
    "requests": [
      { "method": "GET", "path": "/api/v1/subscriber", "total": 55, "mean_ms": 2.1 }
    ]
  },
  "crypto": {
    "vectors": [
      { "type": "eutran", "count": 120, "mean_ms": 0.04, "sum_ms": 5.1 },
      { "type": "eap_aka", "count": 30, "mean_ms": 0.04, "sum_ms": 1.2 }
    ]
  },
  "tac": {
    "cache_size": 8156,
    "imported_total": 8156,
    "lookups": [
      { "result": "hit",  "count": 9820 },
      { "result": "miss", "count": 43  }
    ]
  }
}
```

Latency fields such as `mean_ms`, `sum_ms`, and `wait_duration_ms` are in **milliseconds**. Crypto timings measure only the Milenage computation, not the database SQN increment.

```bash
curl http://localhost:8080/api/v1/oam/metrics
```

---

### Health Check

```
GET /api/v1/oam/health
```

Lightweight liveness probe. Returns 200 whenever the API is reachable. Use this to confirm the HSS is up and measure uptime.

**Response:**

```json
{
  "status": "ok",
  "uptime_seconds": 3600.5,
  "started_at": "2026-03-16T04:14:30Z",
  "version": "0.3.0B"
}
```

| Field | Description |
|-------|-------------|
| `status` | Always `"ok"` when the endpoint responds |
| `uptime_seconds` | Seconds elapsed since the HSS process started |
| `started_at` | RFC 3339 timestamp of when the HSS process started |
| `version` | Running application version |

```bash
curl http://localhost:8080/api/v1/oam/health
```

---

### Version

```
GET /api/v1/oam/version
```

Returns the running binary version and the REST API contract version. Use this to confirm which build is deployed and whether the API surface matches what your client expects.

**Response:**

```json
{
  "app_name": "VectorCore HSS",
  "app_version": "0.3.0B",
  "api_version": "1.0.0"
}
```

| Field | Description |
|-------|-------------|
| `app_name` | Application name  -- always `"VectorCore HSS"` |
| `app_version` | Binary release version. Defaults to `0.3.0B` and is normally injected from the `Makefile` build via `-ldflags`. |
| `api_version` | REST API contract version. Defaults to `1.0.0` and is normally injected from the `Makefile` build via `-ldflags` after manual bumping when the API surface changes. |

```bash
curl http://localhost:8080/api/v1/oam/version
```

---

### Diameter Peers

```
GET /api/v1/oam/diameter/peers
```

Returns a list of Diameter peers that are **directly connected** to this HSS node via a completed CER/CEA handshake. Each entry represents a physical TCP or SCTP connection. Peers that connect through a DRA appear as the DRA's connection, not as individual logical peers behind it. Entries are added on handshake completion and removed when the connection closes.

**Response fields:**

| Field | Description |
|-------|-------------|
| `origin_host` | Diameter `Origin-Host` from the peer's CER |
| `origin_realm` | Diameter `Origin-Realm` from the peer's CER |
| `remote_addr` | Remote socket address (`host:port`) |
| `transport` | Transport protocol: `tcp` or `sctp` |

**Example response:**

```json
{
  "peers": [
    {
      "origin_host": "dra01.epc.mnc435.mcc311.3gppnetwork.org",
      "origin_realm": "epc.mnc435.mcc311.3gppnetwork.org",
      "remote_addr": "10.90.250.35:35500",
      "transport": "tcp"
    },
    {
      "origin_host": "icscf.ims.mnc435.mcc311.3gppnetwork.org",
      "origin_realm": "ims.mnc435.mcc311.3gppnetwork.org",
      "remote_addr": "10.90.250.51:43824",
      "transport": "tcp"
    }
  ]
}
```

```bash
curl http://localhost:8080/api/v1/oam/diameter/peers
```

---

### Diameter Auth Failures

```
GET /api/v1/oam/diameter/auth_failures
```

Returns recent failed S6a AIR authentication attempts captured by the Diameter layer.

**Response:**

```json
{
  "failures": [
    {
      "imsi": "001010000000001",
      "timestamp": "2026-03-16T05:14:30Z",
      "reason": "Roaming not allowed",
      "peer_addr": "10.0.0.15:3868",
      "auth_scope": "roaming",
      "visited_plmn": "130014",
      "visited_mcc": "310",
      "visited_mnc": "410"
    }
  ]
}
```

```bash
curl http://localhost:8080/api/v1/oam/diameter/auth_failures
```

---

### GSUP Peers

```
GET /api/v1/oam/gsup/peers
```

Returns the currently connected GSUP peers. Each entry represents one live IPA/GSUP TCP connection.

**Response fields:**

| Field | Description |
|-------|-------------|
| `name` | Peer name when identified, otherwise the remote address |
| `remote_addr` | Remote socket address (`host:port`) |
| `transport` | Transport protocol, currently `tcp` |

```bash
curl http://localhost:8080/api/v1/oam/gsup/peers
```

---

### SBI Peers

```
GET /api/v1/oam/sbi/peers
```

Returns currently connected SBI peers and forwarded SBI peer metadata. Known peers such as `NRF` and `SCP` are labeled when their addresses are configured; other direct connections fall back to the remote socket address.

**Response fields:**

| Field | Description |
|-------|-------------|
| `name` | Peer name or label when known |
| `remote_addr` | Remote socket address (`host:port`) |
| `transport` | Transport in use, for example `h2c`, `https/h2`, or `h2c via scp` |

```bash
curl http://localhost:8080/api/v1/oam/sbi/peers
```

---

### Backup & Restore

#### Export

```
GET /api/v1/oam/backup
```

Exports all HSS provisioning data as a downloadable JSON file. The response includes a `Content-Disposition: attachment` header so browsers and curl save it as a file automatically.

**What is included:**

| Table | Description |
|---|---|
| `algorithm_profiles` | Custom Milenage profiles |
| `apns` | APN definitions |
| `charging_rules` | Gx charging rules |
| `tfts` | Traffic flow templates |
| `ifc_profiles` | IMS Initial Filter Criteria profiles |
| `eir` | EIR rules |
| `roaming_rules` | Roaming allow/deny rules |
| `aucs` | Authentication credentials (Ki, OPc, SQN) |
| `subscribers` | Subscriber records |
| `subscriber_routing` | Static IP assignments |
| `subscriber_attributes` | Custom subscriber key/value attributes |
| `ims_subscribers` | IMS subscriber records |

**Excluded:** `serving_apn`, `emergency_subscriber`, `eir_history`, `operation_log`, `tac`

**Envelope fields:**

| Field | Description |
|---|---|
| `version` | Backup format version (`"1"`) |
| `exported_at` | UTC timestamp of the export |
| `hss_version` | HSS application version at export time |

```bash
curl -o backup.json http://localhost:8080/api/v1/oam/backup
```

---

#### Restore

```
POST /api/v1/oam/restore
```

Wipes all provisioning data and replaces it with the contents of the backup document, atomically in a single database transaction. PostgreSQL sequences are reset after restore so subsequent API inserts continue from the correct ID. Runtime state (serving APN, emergency subscribers) and the TAC database are not affected.

**Request body:** a `BackupDocument` as produced by `GET /api/v1/oam/backup`.

**Response:** per-table delete and insert counts.

```json
{
  "tables": [
    { "table": "subscriber",          "deleted": 150, "inserted": 150 },
    { "table": "auc",                 "deleted": 150, "inserted": 150 },
    { "table": "apn",                 "deleted":   4, "inserted":   4 },
    { "table": "ims_subscriber",      "deleted": 120, "inserted": 120 },
    { "table": "algorithm_profile",   "deleted":   2, "inserted":   2 }
  ]
}
```

Returns `422 Unprocessable Entity` if the backup `version` field is not `"1"`.

```bash
curl -X POST http://localhost:8080/api/v1/oam/restore \
     -H "Content-Type: application/json" \
     -d @backup.json
```

---

### Operation Log

Read-only audit trail of all database changes made through the OAM API.

#### Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | int | Primary key |
| `item_id` | int | ID of the affected record |
| `operation_id` | string | UUID for grouping related changes |
| `operation` | string | `create`, `update`, or `delete` |
| `changes` | string | JSON diff of changed fields (before/after state) |
| `table_name` | string | Affected table name |
| `timestamp` | string | RFC 3339 UTC |

#### Endpoints

```
GET    /api/v1/oam/operation_log                    List all (newest first)
GET    /api/v1/oam/operation_log/{id}               Get by ID
POST   /api/v1/oam/operation_log/{id}/rollback      Rollback a recorded operation
```

#### Rollback

`POST /api/v1/oam/operation_log/{id}/rollback` reverses a previously logged operation:

| Original operation | Rollback action |
|-------------------|-----------------|
| `create` | DELETE the created record |
| `update` | UPDATE all columns back to the before-snapshot |
| `delete` | INSERT the original record with its original primary key |

Returns `200 {}` on success. Returns `422` if the operation has no change data or the operation type is unknown.

#### Examples

```bash
# List recent operations
curl http://localhost:8080/api/v1/oam/operation_log

# Get a specific entry
curl http://localhost:8080/api/v1/oam/operation_log/42

# Rollback operation #42
curl -X POST http://localhost:8080/api/v1/oam/operation_log/42/rollback
```

---

### Serving APN

Tracks active PGW sessions per subscriber+APN. **Read-only via API**  -- records are created and updated automatically by the Diameter plane on Gx CCR-I and CCR-U, and deleted on CCR-T. Do not create or modify these records manually.

#### Fields

| Field | Type | Description |
|-------|------|-------------|
| `serving_apn_id` | int | Primary key (auto-assigned) |
| `subscriber_id` | int | FK -> Subscriber |
| `apn` | int | FK -> APN ID (0 if APN not configured in DB) |
| `apn_name` | string | Raw APN name from the Gx CCR Called-Station-Id (e.g. `"internet"`, `"ims"`) |
| `pcrf_session_id` | string | PCRF/Gx session identifier |
| `ip_version` | int | 0=IPv4, 1=IPv6, 2=IPv4v6 |
| `ue_ip` | string | UE IP address assigned by P-GW (from Framed-IP-Address in CCR). Used for `{UE_IP}` / `{{UE_IP}}` substitution in TFT flow filters. |
| `serving_pgw` | string | P-GW remote address |
| `serving_pgw_timestamp` | string | Timestamp of last Gx CCR-I/CCR-U |
| `serving_pgw_realm` | string | P-GW Diameter realm |
| `serving_pgw_peer` | string | P-GW Diameter Origin-Host |
| `last_modified` | string | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET  /api/v1/oam/serving_apn                    List all active serving APNs
GET  /api/v1/oam/serving_apn/imsi/{imsi}        Get serving APN by subscriber IMSI
GET  /api/v1/oam/serving_apn/msisdn/{msisdn}    Get serving APN by subscriber MSISDN
GET  /api/v1/oam/serving_apn/ip/{ip}            Get serving APN by UE IP address
```

#### Examples

```bash
# List all active sessions
curl http://localhost:8080/api/v1/oam/serving_apn

# Look up by IMSI
curl http://localhost:8080/api/v1/oam/serving_apn/imsi/001010000000001

# Look up by MSISDN
curl http://localhost:8080/api/v1/oam/serving_apn/msisdn/13135551234

# Look up by UE IP (useful for tracing a session from a network event)
curl http://localhost:8080/api/v1/oam/serving_apn/ip/10.150.3.74
```

---

### Emergency Subscriber

Emergency session tracking. **Read-only via API**  -- records are written automatically by the Gx handler when an unknown UE attaches for emergency services (CCR-I with no matching subscriber), and deleted automatically on session teardown (CCR-T). These represent live emergency sessions, not configuration.

#### Fields

| Field | Type | Description |
|-------|------|-------------|
| `emergency_subscriber_id` | int | Primary key (auto-assigned) |
| `imsi` | string | IMSI of the attaching UE (unique  -- one active emergency session per UE) |
| `serving_pgw` | string | PGW serving the emergency bearer |
| `serving_pcscf` | string | P-CSCF for emergency IMS |
| `gx_origin_realm` | string | Gx origin realm |
| `rat_type` | string | Radio Access Technology type (e.g. `EUTRAN`, `NR`) |
| `ip` | string | Assigned IP address |
| `access_network_gateway_address` | string | ANGA for emergency routing |
| `last_modified` | string | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET  /api/v1/oam/emergency_subscriber          List all active emergency sessions
GET  /api/v1/oam/emergency_subscriber/{id}     Get by ID
```

#### Example

```bash
curl http://localhost:8080/api/v1/oam/emergency_subscriber
```

---

## APN

Access Point Name configuration. Each APN defines QoS parameters and bearer characteristics used in ULA responses.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `apn_id` | int | auto | Primary key (auto-assigned) |
| `apn` | string | yes | APN name (e.g. `internet`) |
| `ip_version` | int | yes | 0=IPv4, 1=IPv6, 2=IPv4v6 |
| `pgw_address` | string | no | P-GW IP address |
| `sgw_address` | string | no | S-GW IP address |
| `charging_characteristics` | string | no | 4-char hex (default `0800`) |
| `apn_ambr_dl` | int | yes | APN downlink AMBR (bps) |
| `apn_ambr_ul` | int | yes | APN uplink AMBR (bps) |
| `qci` | int | no | QoS Class Identifier (default 9) |
| `arp_priority` | int | no | ARP Priority Level 1-15 (default 4) |
| `arp_preemption_capability` | bool | no | ARP preemption capability (default false) |
| `arp_preemption_vulnerability` | bool | no | ARP preemption vulnerability (default false) |
| `charging_rule_list` | string | no | Comma-separated charging rule names |
| `nbiot` | bool | no | NB-IoT APN flag (default false) |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

### Endpoints

```
GET    /api/v1/apn          List all APNs
POST   /api/v1/apn          Create APN
GET    /api/v1/apn/{id}     Get APN by ID
PUT    /api/v1/apn/{id}     Update APN
DELETE /api/v1/apn/{id}     Delete APN
```

### Examples

**Create an APN (minimal  -- server applies defaults):**
```bash
curl -X POST http://localhost:8080/api/v1/apn \
  -H "Content-Type: application/json" \
  -d '{
    "apn": "internet",
    "apn_ambr_dl": 999999999,
    "apn_ambr_ul": 999999999,
    "ip_version": 0
  }'
```

**Response (201):**
```json
{
  "apn_id": 1,
  "apn": "internet",
  "ip_version": 0,
  "charging_characteristics": "0800",
  "apn_ambr_dl": 999999999,
  "apn_ambr_ul": 999999999,
  "qci": 9,
  "arp_priority": 4,
  "arp_preemption_capability": false,
  "arp_preemption_vulnerability": false,
  "nbiot": false,
  "last_modified": "2026-03-16T02:30:41Z"
}
```

**List APNs:**
```bash
curl http://localhost:8080/api/v1/apn
```

**Update an APN:**
```bash
curl -X PUT http://localhost:8080/api/v1/apn/1 \
  -H "Content-Type: application/json" \
  -d '{"apn": "internet", "apn_ambr_dl": 500000000, "apn_ambr_ul": 100000000, "ip_version": 0}'
```

**Delete an APN:**
```bash
curl -X DELETE http://localhost:8080/api/v1/apn/1
```

---

### Charging Rule

Packet data flow (PDF) charging rules applied by the PCRF via Gx. Referenced by APN's `charging_rule_list`.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `charging_rule_id` | int | auto | Primary key (auto-assigned) |
| `rule_name` | string | yes | Rule name (max 20 chars) |
| `qci` | int | no | QoS Class Identifier (default 9) |
| `arp_priority` | int | no | ARP Priority Level (default 4) |
| `arp_preemption_capability` | bool | no | ARP preemption capability (default false) |
| `arp_preemption_vulnerability` | bool | no | ARP preemption vulnerability (default false) |
| `mbr_dl` | int | yes | Maximum Bit Rate downlink (bps) |
| `mbr_ul` | int | yes | Maximum Bit Rate uplink (bps) |
| `gbr_dl` | int | yes | Guaranteed Bit Rate downlink (bps) |
| `gbr_ul` | int | yes | Guaranteed Bit Rate uplink (bps) |
| `tft_group_id` | int | no | FK -> TFT group |
| `precedence` | int | no | Rule precedence |
| `rating_group` | int | no | Charging rating group |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET    /api/v1/apn/charging_rule          List all
POST   /api/v1/apn/charging_rule          Create
GET    /api/v1/apn/charging_rule/{id}     Get by ID
PUT    /api/v1/apn/charging_rule/{id}     Update
DELETE /api/v1/apn/charging_rule/{id}     Delete
```

#### Example

```bash
curl -X POST http://localhost:8080/api/v1/apn/charging_rule \
  -H "Content-Type: application/json" \
  -d '{
    "rule_name": "default-bearer",
    "mbr_dl":    10000000,
    "mbr_ul":    5000000,
    "gbr_dl":    0,
    "gbr_ul":    0
  }'
```

---

### TFT

Traffic Flow Template  -- packet filter rules referenced by a Charging Rule via `tft_group_id`.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tft_id` | int | auto | Primary key (auto-assigned) |
| `tft_group_id` | int | yes | Group ID (links multiple TFTs to one charging rule via `tft_group_id` on the rule) |
| `tft_string` | string | yes | IPFilterRule packet filter. Use `{UE_IP}` or `{{UE_IP}}` as a placeholder for the UE's assigned IP address  -- the HSS substitutes the real IP (from `Framed-IP-Address` in the Gx CCR) at send time. Example: `permit out udp from {UE_IP} to any` |
| `direction` | int | yes | 0=pre-release-7, 1=downlink, 2=uplink, 3=bidirectional |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET    /api/v1/apn/charging_rule/tft          List all
POST   /api/v1/apn/charging_rule/tft          Create
GET    /api/v1/apn/charging_rule/tft/{id}     Get by ID
PUT    /api/v1/apn/charging_rule/tft/{id}     Update
DELETE /api/v1/apn/charging_rule/tft/{id}     Delete
```

#### Example

```bash
curl -X POST http://localhost:8080/api/v1/apn/charging_rule/tft \
  -H "Content-Type: application/json" \
  -d '{"tft_group_id": 1, "tft_string": "permit out ip from any to assigned", "direction": 3}'
```

---

## Subscriber

EPC subscriber profile. Links to an AUC record and one or more APNs. Updated by ULR (serving MME fields) and PUR (cleared on detach).

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subscriber_id` | int | auto | Primary key (auto-assigned) |
| `imsi` | string | yes | IMSI (unique, 15 digits) |
| `enabled` | bool | no | If false, ULR returns DIAMETER_ERROR_UNKNOWN_EPS_SUBSCRIPTION (default true) |
| `auc_id` | int | yes | FK -> AUC record |
| `default_apn` | int | yes | FK -> APN (listed first in ULA subscription data) |
| `apn_list` | string | yes | Comma-separated APN IDs (e.g. `"1,2,3"`) |
| `msisdn` | string | no | MSISDN (optional) |
| `ue_ambr_dl` | int | no | UE downlink AMBR bps (default 999999) |
| `ue_ambr_ul` | int | no | UE uplink AMBR bps (default 999999) |
| `nam` | int | no | Network Access Mode: 0=PACKET\_AND\_CIRCUIT, 2=ONLY\_PACKET (default 0) |
| `access_restriction_data` | int | no | Access-Restriction-Data bitmask (AVP 1426, 3GPP TS 29.272 Table 7.3.31-1). Omit or null for default (0 = all RATs allowed). See table below. |
| `subscribed_rau_tau_timer` | int | no | Periodic RAU/TAU timer seconds (default 300) |
| `roaming_enabled` | bool | no | Roaming flag (default true) |
| `serving_mme` | string | auto | Currently serving MME host (set by ULR, cleared by PUR) |
| `serving_mme_timestamp` | string | auto | Timestamp of last ULR |
| `serving_mme_realm` | string | auto | Serving MME realm |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Access-Restriction-Data bitmask

The `access_restriction_data` field is a bitmask sent to the MME in the ULA Subscription-Data AVP 1426. Each bit restricts a specific RAT type. Set to 0 (or omit) to allow all RATs.

| Bit | Value | Meaning |
|-----|-------|---------|
| 0 | 1 | UTRAN not allowed |
| 1 | 2 | GERAN not allowed |
| 2 | 4 | GAN not allowed |
| 3 | 8 | I-HSPA-Evolution not allowed |
| 4 | 16 | WB-E-UTRAN (LTE) not allowed |
| 5 | 32 | Handover to non-3GPP access not allowed |
| **6** | **64** | **NR as Secondary RAT not allowed (blocks 5G NSA)** |
| 7 | 128 | Unlicensed spectrum as secondary RAT not allowed |
| 8 | 256 | NR in 5GS not allowed (blocks 5G SA) |
| 9 | 512 | Non-3GPP-5GS not allowed |

Common presets:

| Use case | Value |
|----------|-------|
| All RATs allowed (default) | `0` |
| 4G only -- no 5G NSA | `64` |
| 4G only -- no 5G NSA or 5G SA | `320` |
| LTE only -- no UTRAN/GERAN/5G | `67` |

Bits can be combined: e.g. `64 + 2 = 66` means no 5G NSA and no GERAN.

### Endpoints

```
GET    /api/v1/subscriber                  List all subscribers
POST   /api/v1/subscriber                  Create subscriber
GET    /api/v1/subscriber/{id}             Get subscriber by ID
PUT    /api/v1/subscriber/{id}             Update subscriber
DELETE /api/v1/subscriber/{id}             Delete subscriber
GET    /api/v1/subscriber/imsi/{imsi}      Get subscriber by IMSI
POST   /api/v1/subscriber/clr/{imsi}       Send Cancel Location Request
```

### Examples

**Create a subscriber (minimal  -- server applies defaults):**
```bash
curl -X POST http://localhost:8080/api/v1/subscriber \
  -H "Content-Type: application/json" \
  -d '{
    "imsi":        "001010000000001",
    "auc_id":      1,
    "default_apn": 1,
    "apn_list":    "1"
  }'
```

**Disable a subscriber (ULR will be rejected):**
```bash
curl -X PUT http://localhost:8080/api/v1/subscriber/1 \
  -H "Content-Type: application/json" \
  -d '{"imsi": "001010000000001", "enabled": false, "auc_id": 1, "default_apn": 1, "apn_list": "1"}'
```

**Check where a subscriber is currently attached:**
```bash
curl http://localhost:8080/api/v1/subscriber/imsi/001010000000001
```

**Send a Cancel Location Request (force UE detach):**
```bash
curl -X POST http://localhost:8080/api/v1/subscriber/clr/311435000070570
```

Sends a Diameter CLR with `Cancellation-Type = SUBSCRIPTION_WITHDRAWAL` (value 1) to the subscriber's currently serving MME, forcing the UE to detach. The MME's Origin-Host must match an active Diameter connection.

| Error | Meaning |
|-------|---------|
| 422 | Subscriber not found, no serving MME recorded, or MME not currently connected |
| 503 | Diameter layer not available |

**Response (200):**
```json
{
  "imsi": "311435000070570",
  "message": "CLR sent successfully"
}
```

---

### AUC

Authentication Center  -- stores SIM credentials (Ki, OPc, AMF) used by Milenage for LTE authentication vectors.

> **Security:** Ki and OPc are stored as plain hex in the database. Restrict DB and API access accordingly.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `auc_id` | int | auto | Primary key (auto-assigned) |
| `ki` | string | yes | 32-hex Authentication key |
| `opc` | string | yes | 32-hex Operator variant algorithm config |
| `amf` | string | yes | 4-hex Authentication Management Field |
| `sqn` | int | no | Sequence number (managed by Milenage, default 0) |
| `imsi` | string | no | IMSI (unique, links to Subscriber) |
| `iccid` | string | no | SIM card ICCID |
| `batch_name` | string | no | SIM batch identifier |
| `sim_vendor` | string | no | SIM vendor name |
| `esim` | bool | no | eSIM flag (default false) |
| `algo` | string | no | Algorithm: `3`=Milenage (default), `1`/`2`/`4`=Comp128 variants |
| `algorithm_profile_id` | int | no | FK -> Algorithm Profile (custom Milenage c/r constants). Omit or set null to use standard 3GPP Milenage. |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET    /api/v1/subscriber/auc               List all AUC records
POST   /api/v1/subscriber/auc               Create AUC record
GET    /api/v1/subscriber/auc/{id}          Get AUC by ID
PUT    /api/v1/subscriber/auc/{id}          Update AUC record
DELETE /api/v1/subscriber/auc/{id}          Delete AUC record
GET    /api/v1/subscriber/auc/imsi/{imsi}   Get AUC by IMSI
```

#### Examples

**Create an AUC record:**
```bash
curl -X POST http://localhost:8080/api/v1/subscriber/auc \
  -H "Content-Type: application/json" \
  -d '{
    "ki":   "465B5CE8B199B49FAA5F0A2EE238A6BC",
    "opc":  "E8ED289DEBA952E4283B54E88E6183CA",
    "amf":  "8000",
    "imsi": "001010000000001"
  }'
```

**Get AUC by IMSI:**
```bash
curl http://localhost:8080/api/v1/subscriber/auc/imsi/001010000000001
```

**Manually reset SQN (e.g., after SIM re-provisioning):**
```bash
curl -X PUT http://localhost:8080/api/v1/subscriber/auc/1 \
  -H "Content-Type: application/json" \
  -d '{"ki": "465B5CE8B199B49FAA5F0A2EE238A6BC", "opc": "E8ED289DEBA952E4283B54E88E6183CA", "amf": "8000", "sqn": 0, "imsi": "001010000000001"}'
```

---

### Algorithm Profile

Reusable custom Milenage algorithm constant sets. By default the HSS uses the standard 3GPP Milenage constants (c1-c5, r1-r5 from 3GPP TS 35.206). An Algorithm Profile lets you override those constants per AUC record -- useful for SIM vendors that ship cards pre-programmed with non-standard Milenage parameters.

When an AUC record has a non-null `algorithm_profile_id` the HSS uses the custom constants from that profile for all authentication vector generation (AIR Milenage vectors, EAP-AKA vectors, SQN re-sync). When `algorithm_profile_id` is null the standard 3GPP constants are used via the `emakeev/milenage` library.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `algorithm_profile_id` | int | auto | Primary key (auto-assigned) |
| `profile_name` | string | yes | Unique human-readable name (max 128 chars) |
| `c1` | string | yes | 32-hex constant c1 (default standard: `00000000000000000000000000000000`) |
| `c2` | string | yes | 32-hex constant c2 (default standard: `00000000000000000000000000000001`) |
| `c3` | string | yes | 32-hex constant c3 (default standard: `00000000000000000000000000000002`) |
| `c4` | string | yes | 32-hex constant c4 (default standard: `00000000000000000000000000000004`) |
| `c5` | string | yes | 32-hex constant c5 (default standard: `00000000000000000000000000000008`) |
| `r1` | int | yes | Rotation in bits for f1 -- must be a multiple of 8 (default standard: `64`) |
| `r2` | int | yes | Rotation in bits for f2/f5 -- must be a multiple of 8 (default standard: `0`) |
| `r3` | int | yes | Rotation in bits for f3 -- must be a multiple of 8 (default standard: `32`) |
| `r4` | int | yes | Rotation in bits for f4 -- must be a multiple of 8 (default standard: `64`) |
| `r5` | int | yes | Rotation in bits for f5* -- must be a multiple of 8 (default standard: `96`) |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

**Validation:** Each `c` field must be exactly 32 hex characters (128-bit value). Each `r` field must be a multiple of 8 and in the range 0-127.

#### Endpoints

```
GET    /api/v1/subscriber/auc/profile               List all Algorithm Profiles
POST   /api/v1/subscriber/auc/profile               Create Algorithm Profile
GET    /api/v1/subscriber/auc/profile/{id}          Get Algorithm Profile by ID
PUT    /api/v1/subscriber/auc/profile/{id}          Update Algorithm Profile
DELETE /api/v1/subscriber/auc/profile/{id}          Delete Algorithm Profile
```

#### Examples

**Create a profile with standard 3GPP Milenage constants (useful as a named baseline):**
```bash
curl -X POST http://localhost:8080/api/v1/subscriber/auc/profile \
  -H "Content-Type: application/json" \
  -d '{
    "profile_name": "standard-3gpp",
    "c1": "00000000000000000000000000000000",
    "c2": "00000000000000000000000000000001",
    "c3": "00000000000000000000000000000002",
    "c4": "00000000000000000000000000000004",
    "c5": "00000000000000000000000000000008",
    "r1": 64, "r2": 0, "r3": 32, "r4": 64, "r5": 96
  }'
```

**Assign a profile to an AUC record:**
```bash
curl -X PUT http://localhost:8080/api/v1/subscriber/auc/1 \
  -H "Content-Type: application/json" \
  -d '{"ki": "465B5CE8B199B49FAA5F0A2EE238A6BC", "opc": "E8ED289DEBA952E4283B54E88E6183CA", "amf": "8000", "imsi": "001010000000001", "algorithm_profile_id": 1}'
```

**Remove the custom profile from an AUC (revert to standard Milenage):**
```bash
curl -X PUT http://localhost:8080/api/v1/subscriber/auc/1 \
  -H "Content-Type: application/json" \
  -d '{"ki": "465B5CE8B199B49FAA5F0A2EE238A6BC", "opc": "E8ED289DEBA952E4283B54E88E6183CA", "amf": "8000", "imsi": "001010000000001", "algorithm_profile_id": null}'
```

---

### Subscriber Attributes

Extensible key-value store for arbitrary per-subscriber data.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subscriber_attributes_id` | int | auto | Primary key (auto-assigned) |
| `subscriber_id` | int | yes | FK -> Subscriber |
| `key` | string | yes | Attribute name (max 60 chars) |
| `value` | string | yes | Attribute value (max 12,000 chars) |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET    /api/v1/subscriber/attributes          List all
POST   /api/v1/subscriber/attributes          Create
GET    /api/v1/subscriber/attributes/{id}     Get by ID
PUT    /api/v1/subscriber/attributes/{id}     Update
DELETE /api/v1/subscriber/attributes/{id}     Delete
```

#### Example

```bash
curl -X POST http://localhost:8080/api/v1/subscriber/attributes \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id": 1, "key": "customer_ref", "value": "ACME-12345"}'
```

---

### Subscriber Routing

Static IP assignment per subscriber+APN pair. When a record exists, the HSS includes `Framed-IP-Address` in the Gx CCA on CCR-I, directing the PGW to assign that specific IP to the UE instead of allocating dynamically from its pool. If no record exists for the subscriber+APN combination, the PGW assigns an IP dynamically as normal.

Primarily used for Fixed Wireless Access (FWA) deployments where CPE devices require a stable IP address.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subscriber_routing_id` | int | auto | Primary key (auto-assigned) |
| `subscriber_id` | int | yes | FK -> Subscriber |
| `apn_id` | int | yes | FK -> APN |
| `ip_version` | int | yes | 0=IPv4, 1=IPv6, 2=IPv4v6 |
| `ip_address` | string | no | Static IP address |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

Composite unique index on `(subscriber_id, apn_id)`.

#### Endpoints

```
GET    /api/v1/subscriber/routing          List all
POST   /api/v1/subscriber/routing          Create
GET    /api/v1/subscriber/routing/{id}     Get by ID
PUT    /api/v1/subscriber/routing/{id}     Update
DELETE /api/v1/subscriber/routing/{id}     Delete
```

#### Example

```bash
curl -X POST http://localhost:8080/api/v1/subscriber/routing \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id": 1, "apn_id": 1, "ip_version": 0, "ip_address": "10.0.0.100"}'
```

---

## IMS Subscriber

IMS (IP Multimedia Subsystem) subscriber profile. Populated when Cx/Sh interfaces are used.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ims_subscriber_id` | int | auto | Primary key (auto-assigned) |
| `msisdn` | string | yes | MSISDN (unique) |
| `msisdn_list` | string | no | Semicolon-separated additional MSISDNs |
| `imsi` | string | no | Associated IMSI |
| `ifc_profile_id` | int | no | FK -> IFC Profile (Initial Filter Criteria) |
| `scscf` | string | auto | Serving CSCF URI (set by Cx SAR) |
| `pcscf` | string | auto | Proxy CSCF URI (set by Cx UAR) |
| `sh_profile` | string | no | Sh user profile XML blob (optional override) |
| `xcap_profile` | string | no | XCAP profile XML blob |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

> **IFC:** The `ifc_profile_id` field links to an `ifc_profile` record containing the Initial Filter Criteria XML. The Cx SAR and Sh UDR handlers use this to generate 3GPP TS 29.328-compliant Sh-Data XML dynamically at query time. Set `ifc_profile_id` to `null` for subscribers without IFC.

### Endpoints

```
GET    /api/v1/ims_subscriber                  List all IMS subscribers
POST   /api/v1/ims_subscriber                  Create IMS subscriber
GET    /api/v1/ims_subscriber/{id}             Get by ID
PUT    /api/v1/ims_subscriber/{id}             Update
DELETE /api/v1/ims_subscriber/{id}             Delete
GET    /api/v1/ims_subscriber/imsi/{imsi}      Get by IMSI
```

### Example

```bash
curl -X POST http://localhost:8080/api/v1/ims_subscriber \
  -H "Content-Type: application/json" \
  -d '{
    "msisdn":         "447700900001",
    "imsi":           "001010000000001",
    "ifc_profile_id": 1
  }'
```

---

### IFC Profile

Reusable Initial Filter Criteria profiles linked to IMS subscribers via `ifc_profile_id`. The Cx SAR and Sh UDR handlers embed the `xml_data` content inside the `<ServiceProfile>` element of the Sh-Data response at query time  -- one profile can serve all subscribers on the same network.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ifc_profile_id` | int | auto | Primary key (auto-assigned) |
| `name` | string | yes | Human-readable profile name (e.g. `"default-volte"`) |
| `xml_data` | string | yes | IFC inner XML  -- `<PublicIdentity>` blocks and `<InitialFilterCriteria>` blocks only. Do **not** include `<IMSSubscription>`, `<PrivateID>`, or `<ServiceProfile>` wrappers  -- those are generated by the HSS. |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Template Variables

The HSS substitutes the following placeholders in `xml_data` at serve time:

| Placeholder | Replaced with | Source |
|-------------|---------------|--------|
| `{msisdn}` | Subscriber MSISDN | IMS subscriber record |
| `{imsi}` | Subscriber IMSI | IMS subscriber record |
| `{mcc}` | Mobile Country Code | `hss.MCC` in config.yaml |
| `{mnc}` | Mobile Network Code | `hss.MNC` in config.yaml |

Use these to write one profile that works for any subscriber:
```xml
<Identity>sip:{msisdn}@ims.mnc{mnc}.mcc{mcc}.3gppnetwork.org</Identity>
<ServerName>sip:tas.ims.mnc{mnc}.mcc{mcc}.3gppnetwork.org</ServerName>
```

#### Endpoints

```
GET    /api/v1/ims_subscriber/ifc_profile          List all IFC profiles
POST   /api/v1/ims_subscriber/ifc_profile          Create IFC profile
GET    /api/v1/ims_subscriber/ifc_profile/{id}     Get by ID
PUT    /api/v1/ims_subscriber/ifc_profile/{id}     Update
DELETE /api/v1/ims_subscriber/ifc_profile/{id}     Delete
```

#### Example

```bash
# The xml_data must be valid JSON-encoded  -- use a file to avoid quoting issues
python3 -c "
import json
xml = open('/tmp/ifc.xml').read()
print(json.dumps({'name': 'default-volte', 'xml_data': xml}))
" > /tmp/ifc_payload.json

curl -X POST http://localhost:8080/api/v1/ims_subscriber/ifc_profile \
  -H "Content-Type: application/json" \
  -d @/tmp/ifc_payload.json

# Link the profile to an IMS subscriber
curl -X PUT http://localhost:8080/api/v1/ims_subscriber/1 \
  -H "Content-Type: application/json" \
  -d '{"msisdn": "13135551234", "ifc_profile_id": 1}'
```

---

## EIR

Equipment Identity Register  -- IMEI whitelist/blacklist/greylist entries. Used by the S13 Diameter interface (ECR/ECA).

### EIR Response Codes

The `match_response_code` field (and `eir.no_match_response` config value) uses the Equipment-Status values defined in **3GPP TS 29.272 S7.3.51**:

| Code | Name | Meaning |
|------|------|---------|
| `0` | **WHITELISTED** | Device is explicitly allowed. The MME permits the UE to attach. |
| `1` | **BLACKLISTED** | Device is explicitly denied. The MME rejects the UE attach. |
| `2` | **GREYLISTED** | Device is allowed with operator-defined restrictions (e.g. emergency calls only). Handling is MME/operator policy. |

The `eir.no_match_response` config value sets the code returned when **no EIR rule matches** the IMEI. Set to `0` (whitelist all unknown devices) for open networks, or `2` (greylist) if you want the MME to apply restrictions to unrecognised devices. Setting it to `1` would block any device not explicitly whitelisted  -- use with care.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `eir_id` | int | auto | Primary key (auto-assigned) |
| `imei` | string | no | IMEI or regex pattern |
| `imsi` | string | no | IMSI to match |
| `regex_mode` | int | no | `0`=exact match, `1`=regex match on IMEI (default `1`) |
| `match_response_code` | int | yes | See response code table above |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

If neither `imei` nor `imsi` is set, the entry is ignored. When both are set, both must match (exact mode).

### Endpoints

```
GET    /api/v1/eir          List all EIR entries
POST   /api/v1/eir          Create EIR entry
GET    /api/v1/eir/{id}     Get by ID
PUT    /api/v1/eir/{id}     Update
DELETE /api/v1/eir/{id}     Delete
```

### Examples

**Blacklist a specific IMEI:**
```bash
curl -X POST http://localhost:8080/api/v1/eir \
  -H "Content-Type: application/json" \
  -d '{
    "imei": "490154203237518",
    "regex_mode": 0,
    "match_response_code": 1
  }'
```

**Blacklist all IMEIs matching a regex (e.g. a stolen batch):**
```bash
curl -X POST http://localhost:8080/api/v1/eir \
  -H "Content-Type: application/json" \
  -d '{
    "imei": "^49015420.*",
    "regex_mode": 1,
    "match_response_code": 1
  }'
```

---

### EIR History

Read-only log of IMSI/IMEI pairs seen during S13 EIR checks. Populated automatically when `eir.imsi_imei_logging: true` in config. Each unique IMSI+IMEI pair is recorded once and updated (timestamp, response code) on subsequent checks.

#### Fields

| Field | Type | Description |
|-------|------|-------------|
| `imsi_imei_history_id` | int | Primary key |
| `imsi` | string | Subscriber IMSI |
| `imei` | string | Device IMEI (15 digits) |
| `make` | string | Device manufacturer from TAC DB (e.g. `"Apple"`). `"Unknown"` if the IMEI prefix is not in the TAC DB. Empty if `tac_db_enabled: false`. |
| `model` | string | Device model from TAC DB (e.g. `"iPhone 15"`). `"Unknown"` if not found. |
| `match_response_code` | int | EIR result at time of check  -- see [EIR Response Codes](#eir-response-codes) |
| `imsi_imei_timestamp` | string | Timestamp of most recent check (RFC 3339 UTC) |
| `last_modified` | string | RFC 3339 timestamp (auto-set) |

`make` and `model` are populated at write time from the in-memory TAC cache  -- no secondary lookup is needed by the web UI.

#### Endpoints

```
GET    /api/v1/eir/history          List all history entries (newest first)
GET    /api/v1/eir/history/{id}     Get by ID
```

```bash
curl http://localhost:8080/api/v1/eir/history
```

---

### TAC DB

GSMA Type Allocation Code device database. Maps the first 8 digits of an IMEI to a device manufacturer and model name. Used to automatically enrich EIR history records with device info at check time.

> **Note:** "TAC" here means **IMEI Type Allocation Code**  -- completely unrelated to the RAN **Tracking Area Code** which shares the same abbreviation.

Enable with `eir.tac_db_enabled: true` in config (default). When enabled, the full table is loaded into memory at startup for O(1) lookups. The cache is updated automatically on every API write and rebuilt after a CSV import.

#### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tac_id` | int | auto | Primary key (auto-assigned) |
| `tac` | string | yes | 8-digit Type Allocation Code (unique) |
| `make` | string | yes | Device manufacturer (e.g. `"Apple"`) |
| `model` | string | yes | Device model name (e.g. `"iPhone 15"`) |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

#### Endpoints

```
GET    /api/v1/eir/tac                    List TAC entries (supports ?make=, ?model=, ?limit=)
POST   /api/v1/eir/tac                    Create single TAC entry
GET    /api/v1/eir/tac/{tac}             Get by TAC code
PUT    /api/v1/eir/tac/{tac}             Update make/model by TAC code
DELETE /api/v1/eir/tac/{tac}             Delete by TAC code
GET    /api/v1/eir/tac/imei/{imei}       Lookup device by full IMEI (tries 8-digit TAC, falls back to 6-digit)
GET    /api/v1/eir/tac/export            Export full TAC database as CSV download
POST   /api/v1/eir/tac/import            Bulk import from CSV
```

#### List query parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `make` |  -- | Case-insensitive substring filter on manufacturer |
| `model` |  -- | Case-insensitive substring filter on model name |
| `limit` | 100 | Max records returned (1-10000) |

#### CSV Export

`GET /api/v1/eir/tac/export` downloads the full TAC database as a CSV file, sorted by TAC code. The output format matches the import format exactly, so the exported file can be re-imported directly.

```bash
curl -o tac-backup.csv http://localhost:8080/api/v1/eir/tac/export
```

The response includes `Content-Disposition: attachment; filename="hss-tac-TIMESTAMP.csv"` and `Content-Type: text/csv`.

---

#### CSV Import

`POST /api/v1/eir/tac/import` accepts a JSON body with a `csv` field containing the raw CSV text. Imports are idempotent  -- existing TAC codes are updated (upsert). Processed in batches of 500 rows.

CSV format: first three columns must be **TAC** (8 digits), **Make**, **Model**. Additional columns are ignored. Header rows and comment lines are skipped automatically  -- any row whose first field is not all digits is silently skipped.

Compatible with the Osmocom TAC database format (`tac,name,name,contributor,...`).

**Import request:**
```bash
curl -X POST http://localhost:8080/api/v1/eir/tac/import \
  -H "Content-Type: application/json" \
  -d "{\"csv\": \"$(cat /tmp/tacdb.csv | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' | tr -d '\"')\"}"
```

For large files, pass the CSV as a JSON string field. A simpler approach using Python:
```python
import json, requests
csv_text = open('/tmp/tacdb.csv').read()
r = requests.post('http://localhost:8080/api/v1/eir/tac/import',
                  json={'csv': csv_text})
print(r.json())
```

**Import response:**
```json
{
  "inserted": 7842,
  "updated": 0,
  "skipped": 2,
  "errors": 0
}
```

| Field | Description |
|-------|-------------|
| `inserted` | New TAC records added |
| `updated` | Existing records updated with new make/model |
| `skipped` | Rows ignored (header, comment, missing fields) |
| `errors` | DB batch errors (partial failure) |

#### IMEI Lookup

```bash
# Look up a device by full IMEI
curl http://localhost:8080/api/v1/eir/tac/imei/356175060123456

# Response
{
  "tac": "35617506",
  "make": "Oppo",
  "model": "N1"
}
```

Returns 404 if neither the 8-digit nor 6-digit TAC prefix matches any entry.

#### Single record examples

```bash
# Create
curl -X POST http://localhost:8080/api/v1/eir/tac \
  -H "Content-Type: application/json" \
  -d '{"tac": "35617506", "make": "Oppo", "model": "N1"}'

# Update
curl -X PUT http://localhost:8080/api/v1/eir/tac/35617506 \
  -H "Content-Type: application/json" \
  -d '{"make": "Oppo", "model": "N1 Updated"}'

# Delete
curl -X DELETE http://localhost:8080/api/v1/eir/tac/35617506

# Get by TAC code
curl http://localhost:8080/api/v1/eir/tac/35617506

# List all Apple devices
curl "http://localhost:8080/api/v1/eir/tac?make=apple&limit=500"
```

---

## Roaming Rules

Defines per-network roaming allow/deny policy. Each entry specifies a partner network by MCC/MNC and whether roaming to that network is permitted. The global `roaming.allow_undefined_networks` config setting controls the default for networks not listed here.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `roaming_rule_id` | int | auto | Primary key (auto-assigned) |
| `name` | string | no | Descriptive name |
| `mcc` | string | no | Mobile Country Code of the visited network |
| `mnc` | string | no | Mobile Network Code of the visited network |
| `allow` | bool | no | `true` = allow roaming, `false` = deny (default true) |
| `enabled` | bool | no | Rule active flag (default true) |
| `last_modified` | string | auto | RFC 3339 timestamp (auto-set) |

### Endpoints

```
GET    /api/v1/roaming_rules          List all
POST   /api/v1/roaming_rules          Create
GET    /api/v1/roaming_rules/{id}     Get by ID
PUT    /api/v1/roaming_rules/{id}     Update
DELETE /api/v1/roaming_rules/{id}     Delete
```

### Example

```bash
# Deny roaming to a specific network
curl -X POST http://localhost:8080/api/v1/roaming_rules \
  -H "Content-Type: application/json" \
  -d '{"name": "Block Network X", "mcc": "234", "mnc": "30", "allow": false}'
```

---

## GeoRed

Geographic Redundancy (GeoRed) enables active-active replication across multiple HSS nodes. When enabled, each node maintains a full-mesh of HTTP/2 connections to its peers and streams change events in batches.

### OAM Endpoints

These endpoints are available on the standard OAM port (8080) and are only functional when `geored.enabled: true` in config.

```
GET    /api/v1/geored/status         Report peer health, queue depth, and last sync time
POST   /api/v1/geored/sync           Trigger a full snapshot resync with all peers (202 Accepted)
POST   /api/v1/geored/sync/{nodeId}  Trigger resync with a specific peer (204 No Content)
```

#### GET /api/v1/geored/status

Returns an array of peer status objects. Returns `503` if GeoRed is not enabled.

```json
[
  {
    "node_id": "node-b",
    "address": "192.168.1.2:9869",
    "connected": true,
    "queue_depth": 0,
    "last_event_sent": "2026-03-18T12:00:00Z",
    "last_sync": "2026-03-18T11:55:00Z"
  }
]
```

#### POST /api/v1/geored/sync

Triggers an asynchronous full-snapshot resync with all configured peers. Returns `202 Accepted` immediately; replication runs in the background.

#### POST /api/v1/geored/sync/{nodeId}

Triggers resync with the named peer only. Returns `204 No Content` on success, `404` if `nodeId` is not a known peer.

### Inter-Node API

Peers exchange events on a separate port (`geored.listen_port`, default `9869`) using HTTP/2. All requests require a `Authorization: Bearer <token>` header matching `geored.bearer_token`.

```
POST /geored/v1/events     Receive a batch of replication events
GET  /geored/v1/snapshot   Pull a full snapshot from this node
GET  /geored/v1/health     Liveness check (returns {"status":"ok"})
```

Events cover:

| Event type | What it replicates |
|---|---|
| `sqn_update` | SQN increment after AIR/GSUP auth |
| `serving_mme` | Serving MME after ULR (LTE attach) |
| `serving_sgsn` | Serving SGSN after ULR (GERAN/UTRAN) |
| `serving_vlr` | Serving VLR after GSUP UpdateLocation |
| `ims_scscf` | IMS S-CSCF assignment after Cx SAR |
| `ims_pcscf` | IMS P-CSCF assignment after Cx |
| `gx_session_add` | Gx session start (CCR-I) |
| `gx_session_del` | Gx session end (CCR-T) |
| `subscriber_put` | OAM subscriber create/update |
| `subscriber_del` | OAM subscriber delete |
| `auc_put` | OAM AUC create/update |
| `auc_del` | OAM AUC delete |
| `apn_put` | OAM APN create/update |
| `apn_del` | OAM APN delete |
| `ims_sub_put` | OAM IMS subscriber create/update |
| `ims_sub_del` | OAM IMS subscriber delete |
| `eir_put` | OAM EIR entry create/update |
| `eir_del` | OAM EIR entry delete |

### Configuration

```yaml
geored:
  enabled: true
  node_id: "node-a"
  listen_port: 9869
  bearer_token: "changeme"
  tls_cert_file: ""          # blank = cleartext h2c
  tls_key_file:  ""
  sync_oam: true             # replicate OAM changes
  sync_state: true           # replicate dynamic state
  batch_max_events: 500      # flush after this many events
  batch_max_age_ms: 10       # flush after this many ms
  queue_size: 10000          # per-peer outbound queue depth
  periodic_sync_interval_s: 0  # 0 = disabled
  peers:
    - node_id: "node-b"
      address: "192.168.1.2:9869"
      bearer_token: "changeme"
```

---

## Full Provisioning Walkthrough

Minimal steps to provision a subscriber ready for LTE attach:

```bash
BASE=http://localhost:8080/api/v1

# 1. Create APN
APN=$(curl -s -X POST $BASE/apn \
  -H "Content-Type: application/json" \
  -d '{"apn":"internet","apn_ambr_dl":999999999,"apn_ambr_ul":999999999,"ip_version":0}')
APN_ID=$(echo $APN | python3 -c "import sys,json; print(json.load(sys.stdin)['apn_id'])")
echo "APN ID: $APN_ID"

# 2. Create AUC (replace Ki/OPc with real SIM values)
AUC=$(curl -s -X POST $BASE/subscriber/auc \
  -H "Content-Type: application/json" \
  -d "{\"ki\":\"465B5CE8B199B49FAA5F0A2EE238A6BC\",\"opc\":\"E8ED289DEBA952E4283B54E88E6183CA\",\"amf\":\"8000\",\"imsi\":\"001010000000001\"}")
AUC_ID=$(echo $AUC | python3 -c "import sys,json; print(json.load(sys.stdin)['auc_id'])")
echo "AUC ID: $AUC_ID"

# 3. Create Subscriber (minimal  -- defaults applied by server)
curl -s -X POST $BASE/subscriber \
  -H "Content-Type: application/json" \
  -d "{\"imsi\":\"001010000000001\",\"auc_id\":$AUC_ID,\"default_apn\":$APN_ID,\"apn_list\":\"$APN_ID\"}" \
  | python3 -m json.tool
```

---

## Endpoint Summary

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/docs` | Interactive API docs |
| GET | `/api/v1/openapi.yaml` | OpenAPI 3.1 specification |
| GET | `/api/v1/openapi.json` | OpenAPI 3.1 specification (JSON form) |
| GET | `/metrics` | Prometheus text format metrics |
| GET | `/api/v1/oam/health` | Liveness probe  -- status, uptime, version |
| GET | `/api/v1/oam/metrics` | JSON metrics snapshot for dashboards |
| GET | `/api/v1/oam/version` | App and API version |
| GET | `/api/v1/oam/diameter/auth_failures` | List recent S6a authentication failures |
| GET | `/api/v1/oam/operation_log` | List operation log (newest first) |
| GET | `/api/v1/oam/operation_log/{id}` | Get operation log entry |
| POST | `/api/v1/oam/operation_log/{id}/rollback` | Rollback a recorded operation |
| GET | `/api/v1/oam/serving_apn` | List all active 4G serving APNs (read-only) |
| GET | `/api/v1/oam/serving_apn/imsi/{imsi}` | Get serving APN by IMSI |
| GET | `/api/v1/oam/serving_apn/msisdn/{msisdn}` | Get serving APN by MSISDN |
| GET | `/api/v1/oam/serving_apn/ip/{ip}` | Get serving APN by UE IP address |
| GET | `/api/v1/oam/pdu_session` | List all active 5G PDU sessions (read-only) |
| GET | `/api/v1/oam/pdu_session/imsi/{imsi}` | List 5G PDU sessions for a specific IMSI |
| GET | `/api/v1/oam/emergency_subscriber` | List active emergency sessions (read-only) |
| GET | `/api/v1/oam/diameter/peers` | List connected Diameter peers |
| GET | `/api/v1/oam/gsup/peers` | List connected GSUP peers |
| GET | `/api/v1/oam/sbi/peers` | List connected and forwarded SBI peers |
| GET | `/nudm-ueau/v{1,2}/{supi}/security-information/generate-auth-data` | **5G N13** — AUSF: generate 5G-AKA vectors |
| POST | `/nudm-ueau/v{1,2}/{supi}/auth-events` | **5G N13** — AUSF: auth event notification |
| GET | `/nudm-sdm/v{1,2}/{supi}/am-data` | **5G N8** — AMF: AM subscription data |
| GET | `/nudm-sdm/v{1,2}/{supi}/sm-data` | **5G N10** — SMF: SM subscription data |
| GET | `/nudm-sdm/v{1,2}/{supi}/smf-select-data` | **5G N10** — SMF: SMF selection data |
| GET | `/nudm-sdm/v{1,2}/{supi}/nssai` | **5G N8** — AMF: allowed NSSAI |
| POST | `/nudm-sdm/v{1,2}/{supi}/sdm-subscriptions` | **5G N8/N10** — SDM change subscription |
| DELETE | `/nudm-sdm/v{1,2}/{supi}/sdm-subscriptions/{id}` | **5G N8/N10** — SDM unsubscribe |
| PUT | `/nudm-uecm/v1/{supi}/registrations/amf-3gpp-access` | **5G N8** — AMF registration |
| PATCH | `/nudm-uecm/v1/{supi}/registrations/amf-3gpp-access` | **5G N8** — AMF registration update |
| DELETE | `/nudm-uecm/v1/{supi}/registrations/amf-3gpp-access` | **5G N8** — AMF deregistration |
| GET | `/nudm-uecm/v1/{supi}/registrations` | **5G N8** — Get UE registrations |
| PUT | `/nudm-uecm/v1/{supi}/registrations/smf-registrations/{pduSessionId}` | **5G N10** — SMF PDU session registration |
| DELETE | `/nudm-uecm/v1/{supi}/registrations/smf-registrations/{pduSessionId}` | **5G N10** — SMF PDU session deregistration |
| GET | `/nudr-dr/v{1,2}/policy-data/ues/{ueId}/am-data` | **5G N36** — PCF: AM policy data |
| GET | `/nudr-dr/v{1,2}/policy-data/ues/{ueId}/sm-data` | **5G N36** — PCF: SM policy data |
| GET | `/nudr-dr/v{1,2}/policy-data/ues/{ueId}/ue-policy-set` | **5G N36** — PCF: UE policy set |
| GET | `/nudr-dr/v{1,2}/policy-data/sms-management-data/{ueId}` | **5G N36** — PCF: SMS management flags |
| POST | `/npcf-am-policy-control/v1/policies` | **5G N7** — AMF: create AM policy association |
| POST | `/npcf-smpolicycontrol/v1/sm-policies` | **5G N7** — SMF: create SM policy association |
| GET | `/health` | UDM/UDR liveness probe |
| GET | `/api/v1/oam/emergency_subscriber/{id}` | Get emergency session by ID |
| GET | `/api/v1/oam/backup` | Export full database backup (JSON download) |
| POST | `/api/v1/oam/restore` | Import database backup (wipe + restore) |
| GET | `/api/v1/apn` | List APNs |
| POST | `/api/v1/apn` | Create APN |
| GET | `/api/v1/apn/{id}` | Get APN |
| PUT | `/api/v1/apn/{id}` | Update APN |
| DELETE | `/api/v1/apn/{id}` | Delete APN |
| GET | `/api/v1/apn/charging_rule` | List charging rules |
| POST | `/api/v1/apn/charging_rule` | Create charging rule |
| GET | `/api/v1/apn/charging_rule/{id}` | Get charging rule |
| PUT | `/api/v1/apn/charging_rule/{id}` | Update charging rule |
| DELETE | `/api/v1/apn/charging_rule/{id}` | Delete charging rule |
| GET | `/api/v1/apn/charging_rule/tft` | List TFTs |
| POST | `/api/v1/apn/charging_rule/tft` | Create TFT |
| GET | `/api/v1/apn/charging_rule/tft/{id}` | Get TFT |
| PUT | `/api/v1/apn/charging_rule/tft/{id}` | Update TFT |
| DELETE | `/api/v1/apn/charging_rule/tft/{id}` | Delete TFT |
| GET | `/api/v1/subscriber` | List subscribers |
| POST | `/api/v1/subscriber` | Create subscriber |
| GET | `/api/v1/subscriber/{id}` | Get subscriber |
| PUT | `/api/v1/subscriber/{id}` | Update subscriber |
| DELETE | `/api/v1/subscriber/{id}` | Delete subscriber |
| GET | `/api/v1/subscriber/imsi/{imsi}` | Get subscriber by IMSI |
| POST | `/api/v1/subscriber/clr/{imsi}` | Send Cancel Location Request |
| GET | `/api/v1/subscriber/auc` | List AUC records |
| POST | `/api/v1/subscriber/auc` | Create AUC |
| GET | `/api/v1/subscriber/auc/{id}` | Get AUC |
| PUT | `/api/v1/subscriber/auc/{id}` | Update AUC |
| DELETE | `/api/v1/subscriber/auc/{id}` | Delete AUC |
| GET | `/api/v1/subscriber/auc/imsi/{imsi}` | Get AUC by IMSI |
| GET | `/api/v1/subscriber/attributes` | List subscriber attributes |
| POST | `/api/v1/subscriber/attributes` | Create subscriber attribute |
| GET | `/api/v1/subscriber/attributes/{id}` | Get subscriber attribute |
| PUT | `/api/v1/subscriber/attributes/{id}` | Update subscriber attribute |
| DELETE | `/api/v1/subscriber/attributes/{id}` | Delete subscriber attribute |
| GET | `/api/v1/subscriber/routing` | List subscriber routings |
| POST | `/api/v1/subscriber/routing` | Create subscriber routing |
| GET | `/api/v1/subscriber/routing/{id}` | Get subscriber routing |
| PUT | `/api/v1/subscriber/routing/{id}` | Update subscriber routing |
| DELETE | `/api/v1/subscriber/routing/{id}` | Delete subscriber routing |
| GET | `/api/v1/ims_subscriber` | List IMS subscribers |
| POST | `/api/v1/ims_subscriber` | Create IMS subscriber |
| GET | `/api/v1/ims_subscriber/{id}` | Get IMS subscriber |
| PUT | `/api/v1/ims_subscriber/{id}` | Update IMS subscriber |
| DELETE | `/api/v1/ims_subscriber/{id}` | Delete IMS subscriber |
| GET | `/api/v1/ims_subscriber/imsi/{imsi}` | Get IMS subscriber by IMSI |
| GET | `/api/v1/ims_subscriber/ifc_profile` | List IFC profiles |
| POST | `/api/v1/ims_subscriber/ifc_profile` | Create IFC profile |
| GET | `/api/v1/ims_subscriber/ifc_profile/{id}` | Get IFC profile |
| PUT | `/api/v1/ims_subscriber/ifc_profile/{id}` | Update IFC profile |
| DELETE | `/api/v1/ims_subscriber/ifc_profile/{id}` | Delete IFC profile |
| GET | `/api/v1/eir` | List EIR entries |
| POST | `/api/v1/eir` | Create EIR entry |
| GET | `/api/v1/eir/{id}` | Get EIR entry |
| PUT | `/api/v1/eir/{id}` | Update EIR entry |
| DELETE | `/api/v1/eir/{id}` | Delete EIR entry |
| GET | `/api/v1/eir/history` | List EIR audit history |
| GET | `/api/v1/eir/history/{id}` | Get EIR history entry |
| GET | `/api/v1/eir/tac` | List TAC entries |
| POST | `/api/v1/eir/tac` | Create TAC entry |
| GET | `/api/v1/eir/tac/{tac}` | Get TAC entry by code |
| PUT | `/api/v1/eir/tac/{tac}` | Update TAC entry |
| DELETE | `/api/v1/eir/tac/{tac}` | Delete TAC entry |
| GET | `/api/v1/eir/tac/imei/{imei}` | Lookup device by IMEI |
| GET | `/api/v1/eir/tac/export` | Export full TAC database as CSV download |
| POST | `/api/v1/eir/tac/import` | Bulk import TAC records from CSV |
| GET | `/api/v1/roaming_rules` | List roaming rules |
| POST | `/api/v1/roaming_rules` | Create roaming rule |
| GET | `/api/v1/roaming_rules/{id}` | Get roaming rule |
| PUT | `/api/v1/roaming_rules/{id}` | Update roaming rule |
| DELETE | `/api/v1/roaming_rules/{id}` | Delete roaming rule |
| GET | `/api/v1/geored/status` | GeoRed peer status |
| POST | `/api/v1/geored/sync` | Trigger full resync with all peers |
| POST | `/api/v1/geored/sync/{nodeId}` | Trigger resync with a specific peer |
