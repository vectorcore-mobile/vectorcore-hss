# VectorCore HSS

A high-performance Home Subscriber Server (HSS), PCRF, HLR, UDM, UDR, and PCF for 3GPP 2G/3G, LTE/4G, 5GC, and IMS
networks, written in Go.

---

## Features

- **Multi-interface Diameter server** - S6a, S6c, S13, Cx, Sh, Gx, Rx, SWx, SLh, Zh on a single TCP/SCTP listener
- **GSUP/HLR** - Osmocom IPA+GSUP server (port 4222) for OsmoMSC/OsmoSGSN; handles SendAuthInfo, UpdateLocation, PurgeMS; generates 2G triplets and 3G quintuplets for CSFB voice and SGs SMS
- **SMS routing (S6c)** - Send-Routing-Info-for-SM (SRI-SM) resolves MSISDN to serving MME for MT SMS delivery via SGd/T4; Report-SM-Delivery-Status (RSDS) stores Message Waiting Data on delivery failure; Alert-Service-Centre (ALSC) is HSS-initiated and fires automatically when the subscriber re-attaches; SMS-in-MME registration state (MME-Number-for-MT-SMS, MME-Registered-for-SMS) tracked per subscriber via ULR
- **5GC UDM, UDR, and PCF** - N13, N10, N8, N15, N7, and N5 support over a shared SBI listener; UDM, UDR, and PCF are co-located in one process with in-process data sharing; `nudr-dr` / N36 endpoints are exposed for internal service-to-service use
- **VoNR policy (N5)** - Npcf_PolicyAuthorization for the P-CSCF (built to the requests the docker_open5gs Kamailio `WITH_N5` script sends); IMS calls get the same QoS as VoLTE (audio 5QI 1, video 5QI 2, GBR), pushed to the SMF as QoS flows and removed on BYE or PDU session release
- **Milenage authentication** - EUTRAN vector generation (AIR), EAP-AKA vector generation (SWx MAR), GBA vector generation (Zh MAR); custom c/r constant profiles per-AUC
- **Atomic SQN management** - SELECT FOR UPDATE prevents SQN races during concurrent AIR from multiple MMEs
- **In-memory read cache** - 60-second TTL cache on AUC and Subscriber hot paths
- **5G NSA support** - Access-Restriction-Data bitmask per subscriber controls NR secondary RAT access
- **GeoRed** - Active-active N-node geographic redundancy over HTTP/2 with batched event replication; replicates OAM changes and dynamic state (SQN, serving MME/SGSN, Gx sessions) across nodes
- **TAC database** - GSMA IMEI Type Allocation Code database loaded into memory at startup; enriches EIR history with device make and model
- **OAM REST API** - Full CRUD over all subscriber data; OpenAPI 3.1 spec with interactive docs at /api/v1/docs
- **Prometheus metrics** - Diameter request counters and latency histograms at GET /metrics
- **Dual transport** - TCP always on; SCTP optional (requires kernel SCTP module)
- **PostgreSQL + SQLite** - Production on PostgreSQL; SQLite for testing
- **GORM AutoMigrate** - Schema is created or updated on startup; safe to run against existing data
- **Structured logging** - JSON via go.uber.org/zap

---

## Diameter Interfaces

### S6a - LTE HSS <-> MME (3GPP TS 29.272)

| Command | Direction | Description |
|---------|-----------|-------------|
| AIR / AIA | MME -> HSS | Authentication-Information - generates Milenage EUTRAN vectors (RAND, XRES, AUTN, KASME). Supports 1-5 vectors per request and SQN re-synchronisation via AUTS. |
| ULR / ULA | MME -> HSS | Update-Location - registers the serving MME; returns full Subscription-Data (UE-AMBR, NAM, MSISDN, APN-Configuration-Profile with per-APN QoS). |
| PUR / PUA | MME -> HSS | Purge-UE - clears the serving MME on UE detach. |
| NOR / NOA | MME -> HSS | Notify - handles flags-only UE state changes (e.g. UE reachability, MPS registration). |
| CLR / CLA | HSS -> MME | Cancel-Location - sent to the old MME when a subscriber re-attaches to a new one. |
| IDR / IDA | HSS -> MME | Insert-Subscriber-Data - pushes updated subscription data to the serving MME. |
| DSR / DSA | HSS -> MME | Delete-Subscriber-Data - instructs MME to remove subscription data. |
| RSR / RSA | HSS -> MME | Reset - instructs MME to re-authenticate all subscribers after a reset. |

### S13 - Equipment Identity Register (3GPP TS 29.272)

| Command | Direction | Description |
|---------|-----------|-------------|
| ECR / ECA | MME -> HSS | ME-Identity-Check - checks IMEI against the EIR table; returns WHITELISTED (0), BLACKLISTED (1), or GREYLISTED (2). Supports exact and regex match modes. IMSI/IMEI pairs are optionally logged to eir_history. |

### S6c - SMS Routing HSS <-> SMS-SC (3GPP TS 29.338)

| Command | Direction | Description |
|---------|-----------|-------------|
| SRI-SM / SRI-SMA | SMS-SC -> HSS | Send-Routing-Info-for-SM - looks up subscriber by MSISDN (or IMSI, BCD-encoded on the wire); returns IMSI, serving MME address and realm, and MME-Number-for-MT-SMS so the SMS-SC can route the MT SMS via SGd/T4. Returns MWD-Status flags if message waiting data is pending. |
| RSDS / RSDSA | SMS-SC -> HSS | Report-SM-Delivery-Status - on delivery failure (absent subscriber or memory-capacity exceeded) stores a Message Waiting Data record keyed to the SMS-SC; on successful delivery clears the MWD record. Supports MNRF and MCEF status flags per TS 29.338 §5.3.2.4. |
| ALSC / ALSCA | HSS -> SMS-SC | Alert-Service-Centre - HSS-initiated; sent to each SMS-SC with a pending MWD record when the subscriber re-attaches (triggered by ULR success). MWD is deleted only after the SMS-SC returns Result-Code 2001; if the peer is unreachable the MWD is left in place and retried on the next ULR. |

The ULR handler on S6a detects SMS-in-MME capability from ULR-Flags and the MME-Number-for-MT-SMS AVP, stores the registration state on the subscriber record, and sets the MME-Registered-for-SMS bit in ULA-Flags. SRI-SM returns this MME number in the Serving-Node grouped AVP so the SMS-SC can route directly to the MME.

### Cx - IMS HSS <-> CSCF (3GPP TS 29.228 / 29.229)

| Command | Direction | Description |
|---------|-----------|-------------|
| UAR / UAA | CSCF -> HSS | User-Authorization - checks if a public identity is allowed to register at the requesting CSCF. |
| SAR / SAA | CSCF -> HSS | Server-Assignment - records the S-CSCF serving a public identity; returns the IMS subscriber's Cx User-Data XML including IFC. |
| LIR / LIA | CSCF -> HSS | Location-Info - returns the S-CSCF name for a given public identity. |
| MAR / MAA | CSCF -> HSS | Multimedia-Authentication - generates SIP Digest or AKA authentication data. |
| RTR | HSS -> CSCF | Registration-Termination - forces de-registration of a public identity. |
| PPR | HSS -> CSCF | Push-Profile - pushes an updated IMS profile to the serving CSCF. |

### Sh - IMS HSS <-> Application Server (3GPP TS 29.328 / 29.329)

| Command | Direction | Description |
|---------|-----------|-------------|
| UDR / UDA | AS -> HSS | User-Data - returns Sh-Data XML (AVP 702) for a subscriber. Dynamically renders IMPU, IMPI, S-CSCF name, IMS user state, and IFC (IFC only returned for Data-Reference=13). Supports RepositoryData (AS-stored blob) and all standard Data-Reference values (0, 10-17). |
| PNR / PNA | HSS -> AS | Push-Notification - pushes updated subscriber data to the Application Server. |

### Gx - PCRF <-> PGW (3GPP TS 29.212)

| Command | Direction | Description |
|---------|-----------|-------------|
| CCR / CCA | PGW -> HSS | Credit-Control - handles session establishment (CCR-I), modification (CCR-U), and termination (CCR-T). Tracks active PGW sessions in serving_apn. |
| RAR / RAA | HSS -> PGW | Re-Auth - installs or removes dedicated bearers on an active Gx session; triggered by AAR/STR from the P-CSCF via Rx. |

### Rx - P-CSCF <-> PCRF (3GPP TS 29.214)

| Command | Direction | Description |
|---------|-----------|-------------|
| AAR / AAA | P-CSCF -> HSS | AA-Request - provisions QoS resources for a VoLTE media session; triggers a Gx RAR to the PGW to install a dedicated bearer. |
| STR / STA | P-CSCF -> HSS | Session-Termination - tears down the Rx session and triggers a Gx RAR to the PGW to remove the dedicated bearer. |

### SWx - AAA Server <-> HSS for non-3GPP WiFi (3GPP TS 29.273)

| Command | Direction | Description |
|---------|-----------|-------------|
| MAR / MAA | AAA -> HSS | Multimedia-Authentication - generates EAP-AKA vectors (RAND, XRES, AUTN, CK, IK) for non-3GPP WiFi authentication. |
| SAR / SAA | AAA -> HSS | Server-Assignment - returns Non-3GPP-User-Data (access allowed or barred based on subscriber status). |
| RTR | HSS -> AAA | Registration-Termination - de-registers a non-3GPP session. |
| PPR | HSS -> AAA | Push-Profile - pushes an updated non-3GPP access profile to the AAA server. |

### SLh - GMLC <-> HSS for Location Services (3GPP TS 29.173)

| Command | Direction | Description |
|---------|-----------|-------------|
| RIR / RIA | GMLC -> HSS | LCS-Routing-Info-Request/Answer (application ID `16777291`, command code `8388622`) returns serving-node routing information. |

The GMLC uses the returned MME identity to route its SLg PLR to the MME. The
HSS learns LTE MME name and realm from S6a ULR registration state. An attached
subscriber returns `MME-Name` and `MME-Realm`; an existing subscriber without
a usable serving node returns `DIAMETER_ERROR_ABSENT_USER` (4201), and an
unknown subscriber returns `DIAMETER_ERROR_USER_UNKNOWN` (5001).

SLh authorizes the requesting GMLC by `Origin-Realm`, compared case
insensitively with a trailing dot normalized. The HSS's own `OriginRealm` is
always authorized. `hss.SLhAuthorizedRealms` adds specific external GMLC
realms; an empty list authorizes no external realm. Unauthorized requests
return `DIAMETER_ERROR_UNAUTHORIZED_REQUESTING_NETWORK` (5490). SLh normally
uses SCTP; set `hss.EnableSCTP: true` (and provide kernel SCTP support) to
enable the shared SCTP listener. TCP remains available for the other shared
Diameter interfaces and lab interoperability.

### Zh - BSF <-> HSS for GBA (3GPP TS 29.109)

| Command | Direction | Description |
|---------|-----------|-------------|
| MAR / MAA | BSF -> HSS | Multimedia-Authentication - generates GBA bootstrapping vectors for Generic Bootstrapping Architecture. |

---

## GSUP / HLR Interface

The GSUP server (port 4222 by default) implements the Osmocom IPA+GSUP protocol
for 2G/3G circuit-switched authentication. It is used by OsmoMSC, OsmoSGSN, and
OsmoSTP for CSFB voice and SGs SMS handling.

| Message | Direction | Description |
|---------|-----------|-------------|
| SendAuthInfo | MSC/SGSN -> HSS | Generates 2G triplets (RAND, SRES, Kc) or 3G quintuplets (RAND, XRES, CK, IK, AUTN) from Milenage. |
| UpdateLocation | MSC/SGSN -> HSS | Registers the serving MSC or SGSN; returns subscriber data. |
| PurgeMS | MSC/SGSN -> HSS | Clears the serving MSC/SGSN on detach. |

---

## Geographic Redundancy (GeoRed)

VectorCore HSS supports active-active N-node geographic redundancy. Each node
replicates changes to its peers over HTTP/2 using batched event streams.

Two replication tracks are supported:

- **OAM sync** - subscriber, AUC, APN, IMS subscriber, and EIR record changes
- **State sync** - SQN increments, serving MME/SGSN assignments, Gx sessions

Events are batched by count (default 500) or age (default 10 ms) and delivered
to each peer with bearer token authentication. Nodes are symmetric - any node
can accept writes and will replicate to all others.

---

## Roaming Control

Roaming is enforced at the Diameter layer on both AIR and ULR. When a subscriber
attaches via a visited PLMN the HSS evaluates the following rules in order:

1. **Home network** - always allowed (visited PLMN matches home MCC/MNC).
2. **Subscriber roaming switch** - if `roaming_enabled = false` on the subscriber record the attach is rejected with ROAMING_NOT_ALLOWED.
3. **Network rule** - the visited MCC/MNC is looked up in `roaming_rules`. If a matching rule exists its `allow` flag is applied.
4. **Undefined networks** - if no rule matches, the `roaming.allow_undefined_networks` config flag controls whether the attach is permitted (default: true).

Roaming rules are managed via the OAM REST API (`/api/v1/roaming_rules`).

Config flag:

```yaml
roaming:
  allow_undefined_networks: true   # allow roaming on networks with no explicit rule
```

---

## OAM REST API

Served on port 8080 (configurable). Interactive documentation at `http://<host>:8080/api/v1/docs`.

See [docs/api.md](docs/api.md) for the full reference.

| Resource | Path | Description |
|----------|------|-------------|
| APN | `/api/v1/apn` | Access Point Name profiles |
| AUC | `/api/v1/subscriber/auc` | Authentication Center (Ki, OPc, AMF, SQN) |
| Algorithm Profile | `/api/v1/subscriber/auc/profile` | Custom Milenage c/r constant sets |
| Subscriber | `/api/v1/subscriber` | EPC subscriber profiles |
| IMS Subscriber | `/api/v1/ims_subscriber` | IMS subscriber profiles |
| IFC Profile | `/api/v1/ims_subscriber/ifc_profile` | Initial Filter Criteria XML blobs |
| EIR | `/api/v1/eir` | Equipment Identity Register entries |
| EIR History | `/api/v1/eir/history` | IMSI/IMEI audit log (read-only) |
| Roaming Rules | `/api/v1/roaming_rules` | Per-network allow/deny rules enforced on AIR and ULR |
| Charging Rule | `/api/v1/apn/charging_rule` | Gx charging rules |
| TFT | `/api/v1/apn/charging_rule/tft` | Traffic Flow Templates |
| Subscriber Routing | `/api/v1/subscriber/routing` | Static IP assignments per subscriber+APN |
| Serving APN | `/api/v1/oam/serving_apn` | Active PGW sessions |
| Subscriber Attributes | `/api/v1/subscriber/attributes` | Key-value store per subscriber |
| Emergency Subscriber | `/api/v1/oam/emergency_subscriber` | Emergency session tracking |
| Operation Log | `/api/v1/oam/operation_log` | Audit trail (read-only) |
| GeoRed Status | `/api/v1/geored/status` | Active replication peer status |
| GeoRed Sync | `/api/v1/geored/sync` | Trigger a full resync to all peers |

---

## Quick Start

### Prerequisites

- Go 1.25+
- PostgreSQL 14+ (or SQLite for testing)

### Build

```bash
make build
```

Output: `bin/hss`

Optional test tools:

```bash
make n5sim      # bin/n5sim - N5 / PCF test tool (plays the Kamailio P-CSCF and the SMF; see docs/api.md)
```

### Configure

The full config.yaml with all sections:

```yaml
hss:
  OriginHost: hss01.epc.mnc001.mcc001.3gppnetwork.org
  OriginRealm: epc.mnc001.mcc001.3gppnetwork.org
  BindAddress: "::"
  BindPort: 3868
  EnableSCTP: false      # set true to also listen on SCTP (requires kernel sctp module)
  DWRInterval: 30
  CancelLocationRequest_Enabled: true
  MCC: "001"
  MNC: "01"
  scscf_pool:
    - 'sip:scscf.ims.mnc001.mcc001.3gppnetwork.org'
  # TLSCertFile: /etc/hss/tls/cert.pem   # uncomment for DiameterTLS (RFC 6733)
  # TLSKeyFile:  /etc/hss/tls/key.pem

database:
  db_type: postgresql
  server: localhost
  port: 5432
  username: hss
  password: hss
  database: hss
  pool_size: 30
  pool_idle: 10
  pool_recycle: 300

logging:
  # Supported levels: debug, info, warn, error
  level: info
  file: /var/log/hss.log   # write logs to this file (comment out to disable)
  # Use -d flag to force debug level and enable console output

eir:
  # no_match_response: response code when an IMEI has no matching EIR rule.
  # EIR response codes (3GPP TS 29.272 §7.3.51 Equipment-Status):
  #   0 = WHITELISTED  device is allowed (default)
  #   1 = BLACKLISTED  device is denied
  #   2 = GREYLISTED   device is allowed with operator-defined restrictions
  no_match_response: 0
  imsi_imei_logging: true
  # tac_db_enabled loads the GSMA Type Allocation Code (IMEI device database)
  # into memory and enriches EIR history with device make/model.
  # NOTE: TAC here = IMEI Type Allocation Code, NOT the RAN Tracking Area Code.
  tac_db_enabled: true

geored:
  enabled: false
  node_id: "hss01"           # unique identifier for this node (required when enabled)
  listen_port: 9869           # inter-node listener port (h2c or TLS)
  bearer_token: "changeme"    # shared token for inter-node auth
  # tls_cert_file: ""           # leave blank for cleartext h2c
  # tls_key_file:  ""
  sync_oam: true               # replicate OAM changes (subscriber, AUC, APN, IMS, EIR)
  sync_state: true             # replicate dynamic state (SQN, serving MME/SGSN, Gx sessions)
  batch_max_events: 500        # flush batch after this many events
  batch_max_age_ms: 10         # flush batch after this many milliseconds
  queue_size: 10000            # per-peer outbound queue depth
  periodic_sync_interval_s: 0  # 0 = disabled; >0 triggers a full resync every N seconds
  peers: []
  # peers:
  #   - node_id: "hss02"
  #     address: "192.168.1.2:9869"
  #     bearer_token: "changeme"

gsup:
  # GSUP/HLR listener for Osmocom MSC/SGSN (OsmoMSC, OsmoSGSN).
  # Used for 2G/3G circuit-switched auth: SGs SMS and CSFB voice.
  # Protocol: Osmocom IPA + GSUP over TCP (default port 4222).
  enabled: true
  bind_address: "::"
  bind_port: 4222

5gc:
  sbi:
    bind_address: "::"
    bind_port: 7777
    # Optional TLS for HTTPS + HTTP/2.
    # tls_cert_file: /etc/hss/tls/5gc.crt
    # tls_key_file:  /etc/hss/tls/5gc.key
    oauth2_enabled: false
    oauth2_bypass: true
    client:
      mode: direct
      scp_address: ""   # used when mode: scp
      # Fixed delay between NRF registration retry attempts.
      reconnect_holdoff_time: 2s

  udm:
    enabled: false
    mcc: "001"
    mnc: "01"
    nrf_address: ""
    nf_instance_id: ""
    suci_decryption_keys: []

  pcf:
    enabled: false
    mcc: "001"
    mnc: "01"
    nrf_address: ""
    nf_instance_id: ""

api:
  enabled: true
  bind_address: "::"
  bind_port: 8080
  # tls_cert_file: /etc/hss/tls/cert.pem   # uncomment for HTTPS
  # tls_key_file:  /etc/hss/tls/key.pem
  auth_enabled: false
  api_keys: []
  # api_keys:
  #   - "your-secret-key-here"
```

### Run

```bash
./bin/hss -c  config.yaml
```

On startup the server will:

1. Connect to PostgreSQL and run AutoMigrate on all tables
2. Load the GSMA TAC database into memory (if tac_db_enabled)
3. Start the Diameter listener on TCP port 3868 (and SCTP if enabled)
4. Start the GSUP/HLR listener on TCP port 4222 (if enabled)
5. Start the shared 5GC SBI listener on port 7777 for UDM/UDR/PCF (if enabled)
6. Start the OAM REST API on port 8080 (if enabled)
7. Start the GeoRed replication listener on port 9869 (if enabled)

### Provision a subscriber

```bash
BASE=http://localhost:8080

# 1. Create APN
APN_ID=$(curl -s -X POST $BASE/apn \
  -H "Content-Type: application/json" \
  -d '{"apn":"internet","apn_ambr_dl":999999999,"apn_ambr_ul":999999999,"ip_version":0}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['apn_id'])")

# 2. Create AUC (replace Ki/OPc with your SIM values)
AUC_ID=$(curl -s -X POST $BASE/auc \
  -H "Content-Type: application/json" \
  -d '{"ki":"465B5CE8B199B49FAA5F0A2EE238A6BC","opc":"E8ED289DEBA952E4283B54E88E6183CA","amf":"8000","imsi":"001010000000001"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['auc_id'])")

# 3. Create EPC subscriber
curl -s -X POST $BASE/subscriber \
  -H "Content-Type: application/json" \
  -d "{\"imsi\":\"001010000000001\",\"auc_id\":$AUC_ID,\"default_apn\":$APN_ID,\"apn_list\":\"$APN_ID\",\"msisdn\":\"0000000001\"}"

# 4. Create IMS subscriber (links to EPC subscriber by MSISDN)
curl -s -X POST $BASE/ims_subscriber \
  -H "Content-Type: application/json" \
  -d '{"msisdn":"0000000001","imsi":"001010000000001"}'
```

---

## Architecture

```
MME / CSCF / PGW / AAA / AS / BSF / GMLC (Diameter peers)
  |
  +-- internal/diameter/server.go        StateMachine, handler wiring, TCP/SCTP listener
        |
        +-- internal/diameter/s6a/       AIR, ULR, PUR, NOR; HSS-originated CLR, IDR, DSR, RSR
        +-- internal/diameter/s6c/       SRI-SM, RSDS; HSS-originated ALSC (SMS routing + MWD)
        +-- internal/diameter/s13/       ECR (EIR check)
        +-- internal/diameter/cx/        UAR, SAR, LIR, MAR; HSS-originated RTR, PPR
        +-- internal/diameter/sh/        UDR; HSS-originated PNR
        +-- internal/diameter/gx/        CCR; HSS-originated RAR
        +-- internal/diameter/rx/        AAR, STR; triggers Gx RAR to PGW for VoLTE bearers
        +-- internal/diameter/swx/       MAR, SAR; HSS-originated RTR, PPR
        +-- internal/diameter/slh/       RIR/RIA (GMLC location routing)
        +-- internal/diameter/zh/        MAR (GBA bootstrapping)

OsmoMSC / OsmoSGSN (GSUP)
  |
  +-- internal/gsup/                     SendAuthInfo, UpdateLocation, PurgeMS over IPA+GSUP

internal/crypto/auth.go                  Milenage: EUTRAN vectors, EAP-AKA, 2G/3G triplets/quintuplets
internal/ims/shprofile.go                3GPP TS 29.328 Sh-Data XML renderer (IMPU, IMPI, SCSCF, IFC)
internal/ims/cxprofile.go                3GPP TS 29.228 Cx User-Data XML renderer
internal/repository/postgres/store.go   Repository: PostgreSQL via GORM + sync.Map cache (60s TTL)
internal/geored/                         Active-active N-node replication over HTTP/2
internal/api/                            OAM REST API (huma/v2, chi, OpenAPI 3.1)
internal/metrics/                        Prometheus counters and histograms
internal/taccache/                       GSMA TAC database in-memory cache
```

**Request flow (AIR example):**

```
AIR received on TCP/SCTP
  -> server.go              logging, metrics wrap, peer tracking
  -> s6a/air.go             parse request, validate IMSI
  -> repository             GetAUCByIMSI (cache hit or PostgreSQL)
  -> crypto/auth.go         GenerateEUTRANVectors (Milenage x N)
    -> repository           AtomicGetAndIncrementSQN (SELECT FOR UPDATE)
  -> s6a/air.go             build AIA with E-UTRAN-Vector AVPs
  -> geored                 PublishSQNUpdate (replicate to peers if GeoRed enabled)
  -> server.go              write AIA to connection
```

---

## Technology Stack

| Component | Library |
|-----------|---------|
| Diameter protocol | `github.com/fiorix/go-diameter/v4` |
| Milenage / EAP-AKA | `github.com/emakeev/milenage` |
| ORM | `gorm.io/gorm` + `gorm.io/driver/postgres` |
| REST API framework | `github.com/danielgtaylor/huma/v2` (OpenAPI 3.1) |
| HTTP router | `github.com/go-chi/chi/v5` |
| Metrics | `github.com/prometheus/client_golang` |
| Logging | `go.uber.org/zap` |
| Config | `gopkg.in/yaml.v3` |

---

## Database

Supports **PostgreSQL** (production) and **SQLite** (testing). Set `database.db_type`
to `postgresql` or `sqlite` in config.yaml. GORM AutoMigrate runs on every
startup - it is additive-only and safe to run against an existing PyHSS database.

**Tables:** `apn`, `auc`, `algorithm_profile`, `subscriber`, `subscriber_routing`,
`serving_apn`, `ims_subscriber`, `ifc_profile`, `roaming_network`, `roaming_rule`,
`emergency_subscriber`, `charging_rule`, `tft`, `eir`, `eir_history`,
`subscriber_attributes`, `operation_log`, `message_waiting_data`

---

## Documentation

| Document | Description |
|----------|-------------|
| [docs/api.md](docs/api.md) | Full OAM REST API reference - all endpoints, fields, and curl examples |
