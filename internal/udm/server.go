package udm

// server.go — VectorCore UDR/UDM server.
//
// Implements the 3GPP Nudm SBI interfaces used by Open5GS AUSF, AMF, and SMF.
// VectorCore acts as both UDM (application logic) and UDR (data repository) —
// there is no separate UDR process; we go directly to PostgreSQL.
//
// Interfaces served:
//   nudm-ueau  — Nudm_UEAuthentication  (AUSF calls this for 5G-AKA vectors)
//   nudm-sdm   — Nudm_SDM              (AMF/SMF fetch subscription data)
//   nudm-uecm  — Nudm_UECM             (AMF/SMF register UE context)
//
// Transport: HTTP/2.  TLS absent → h2c (cleartext HTTP/2, typical for labs).
//            TLS present → TLS + h2 (production).

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/peertracker"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
	"github.com/svinson1121/vectorcore-hss/internal/taccache"
)

// Server is the UDR/UDM HTTP/2 server.
type Server struct {
	cfg   config.UDMConfig
	store repository.Repository
	log   *zap.Logger
	jwks  *sbi.JWKSStore
	hnet  *HNetKeyStore
	pt    *peertracker.Tracker
	fpt   *peertracker.Tracker
	tac   *taccache.Cache

	eirNoMatchResp int
	eirIMSIIMEILog bool
}

// New creates a new UDM server.
// If cfg.NFInstanceID is empty a random UUID v4 is generated so the NRF
// registration always has a stable-per-process identity.
func New(cfg config.UDMConfig, store repository.Repository, log *zap.Logger) *Server {
	if cfg.NFInstanceID == "" {
		cfg.NFInstanceID = sbi.NewNFInstanceID()
		log.Info("udm: generated NFInstanceID", zap.String("nf_instance_id", cfg.NFInstanceID))
	}

	hnet, err := LoadHNetKeys(cfg.SUCIDecryptionKeys)
	if err != nil {
		log.Fatal("udm: failed to load HNet keys", zap.Error(err))
	}
	if len(cfg.SUCIDecryptionKeys) > 0 {
		log.Info("udm: loaded SUCI decryption keys", zap.Int("count", len(cfg.SUCIDecryptionKeys)))
	}

	return &Server{
		cfg:   cfg,
		store: store,
		log:   log,
		jwks:  &sbi.JWKSStore{},
		hnet:  hnet,
		pt:    peertracker.New(),
		fpt:   peertracker.NewWithMaxAge(2 * time.Minute),
	}
}

func (s *Server) UsePeerTrackers(pt, fpt *peertracker.Tracker) {
	s.pt = pt
	s.fpt = fpt
}

func (s *Server) Config() config.UDMConfig {
	return s.cfg
}

func (s *Server) WithTAC(c *taccache.Cache) *Server {
	s.tac = c
	return s
}

func (s *Server) WithEIR(noMatchResp int, imsiIMEILog bool) *Server {
	s.eirNoMatchResp = noMatchResp
	s.eirIMSIIMEILog = imsiIMEILog
	return s
}

func (s *Server) MountRoutes(r *chi.Mux) {
	s.mountRoutes(r)
}

func (s *Server) StartNRFRegistration() {
	s.startNRFRegistration()
}

// Peers returns the live SBI peer tracker.
func (s *Server) Peers() *peertracker.Tracker {
	if s.pt == nil {
		s.pt = peertracker.New()
	}
	return s.pt
}

func (s *Server) ForwardedPeers() *peertracker.Tracker {
	if s.fpt == nil {
		s.fpt = peertracker.NewWithMaxAge(2 * time.Minute)
	}
	return s.fpt
}

// Start begins listening.  Blocks until a fatal error.
func (s *Server) Start() error {
	r := chi.NewRouter()
	s.mountRoutes(r)

	addr := net.JoinHostPort(s.cfg.BindAddress, fmt.Sprintf("%d", s.cfg.BindPort))
	s.log.Info("udm: UDR/UDM listening", zap.String("addr", addr))

	// Start NRF registration in the background.
	go s.startNRFRegistration()

	if s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("udm: load TLS cert: %w", err)
		}
		srv := &http.Server{
			Addr:    addr,
			Handler: sbi.InboundMetadataMiddleware(s.ForwardedPeers(), "https/h2")(r),
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				NextProtos:   []string{"h2"},
			},
			ConnState: s.connState("https/h2"),
		}
		http2.ConfigureServer(srv, nil)
		return srv.ListenAndServeTLS("", "")
	}

	// Cleartext HTTP/2 (h2c) — the normal mode for Open5GS lab deployments.
	srv := &http.Server{
		Addr:      addr,
		Handler:   h2c.NewHandler(sbi.InboundMetadataMiddleware(s.ForwardedPeers(), "h2c")(r), &http2.Server{}),
		ConnState: s.connState("h2c"),
	}
	return srv.ListenAndServe()
}

func (s *Server) connState(transport string) func(net.Conn, http.ConnState) {
	return func(conn net.Conn, state http.ConnState) {
		remote := conn.RemoteAddr().String()
		switch state {
		case http.StateNew, http.StateActive, http.StateIdle:
			s.Peers().Add(peertracker.Peer{
				Name:       sbi.PeerDisplayName(remote, s.cfg.SBIClient.SCPAddress),
				RemoteAddr: remote,
				Transport:  transport,
			})
		case http.StateHijacked, http.StateClosed:
			s.Peers().Remove(remote)
		}
	}
}

// mountRoutes registers all Nudm endpoints on the chi router.
// Both v1 and v2 URL prefixes are mounted to the same handlers.
func (s *Server) mountRoutes(r *chi.Mux) {
	for _, ver := range []string{"v1", "v2"} {
		// nudm-ueau — UE authentication (AUSF)
		ueau := fmt.Sprintf("/nudm-ueau/%s/{supi}", ver)
		r.Post(ueau+"/security-information/generate-auth-data",
			s.wrapOAuth("nudm-ueau", s.handleGenerateAuthData))
		r.Post(ueau+"/auth-events",
			s.wrapOAuth("nudm-ueau", s.handleAuthEvent))

		// nudm-sdm — Subscription data management (AMF, SMF)
		sdm := fmt.Sprintf("/nudm-sdm/%s/{supi}", ver)
		r.Get(sdm,
			s.wrapOAuth("nudm-sdm", s.handleGetDataSets))
		r.Get(sdm+"/am-data",
			s.wrapOAuth("nudm-sdm", s.handleAMData))
		r.Get(sdm+"/sm-data",
			s.wrapOAuth("nudm-sdm", s.handleSMData))
		r.Get(sdm+"/smf-select-data",
			s.wrapOAuth("nudm-sdm", s.handleSMFSelectData))
		r.Get(sdm+"/nssai",
			s.wrapOAuth("nudm-sdm", s.handleNSSAI))
		r.Get(sdm+"/ue-context-in-smf-data",
			s.wrapOAuth("nudm-sdm", s.handleUEContextInSMFData))
		r.Post(sdm+"/sdm-subscriptions",
			s.wrapOAuth("nudm-sdm", s.handleSDMSubscribe))
		r.Delete(sdm+"/sdm-subscriptions/{subscriptionId}",
			s.wrapOAuth("nudm-sdm", s.handleSDMUnsubscribe))
	}

	// nudm-uecm — UE context management (AMF, SMF) — v1 only in Open5GS
	for _, ver := range []string{"v1"} {
		uecm := fmt.Sprintf("/nudm-uecm/%s/{supi}/registrations", ver)
		r.Put(uecm+"/amf-3gpp-access",
			s.wrapOAuth("nudm-uecm", s.handleAMFRegistrationPut))
		r.Patch(uecm+"/amf-3gpp-access",
			s.wrapOAuth("nudm-uecm", s.handleAMFRegistrationPatch))
		r.Delete(uecm+"/amf-3gpp-access",
			s.wrapOAuth("nudm-uecm", s.handleAMFRegistrationDelete))
		r.Get(uecm,
			s.wrapOAuth("nudm-uecm", s.handleGetRegistrations))
		r.Put(uecm+"/smf-registrations/{pduSessionId}",
			s.wrapOAuth("nudm-uecm", s.handleSMFRegistrationPut))
		r.Delete(uecm+"/smf-registrations/{pduSessionId}",
			s.wrapOAuth("nudm-uecm", s.handleSMFRegistrationDelete))
	}

	// nudr-dr — UDR Data Management (N36: PCF → UDR, N37: NEF → UDR)
	for _, ver := range []string{"v1", "v2"} {
		pd := fmt.Sprintf("/nudr-dr/%s/policy-data/ues/{ueId}", ver)
		r.Get(pd+"/am-data",
			s.wrapOAuth("nudr-dr", s.handleUDRAMPolicyData))
		r.Get(pd+"/sm-data",
			s.wrapOAuth("nudr-dr", s.handleUDRSMPolicyData))
		r.Get(pd+"/ue-policy-set",
			s.wrapOAuth("nudr-dr", s.handleUDRUEPolicySet))
		r.Get(fmt.Sprintf("/nudr-dr/%s/policy-data/sms-management-data/{ueId}", ver),
			s.wrapOAuth("nudr-dr", s.handleUDRSMSManagement))
	}

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","nf":"UDM/UDR"}`))
	})
}

// wrapOAuth wraps a handler with OAuth2 middleware for the given scope.
func (s *Server) wrapOAuth(scope string, h http.HandlerFunc) http.HandlerFunc {
	wrapped := sbi.OAuthMiddleware(s.cfg.OAuth2Enabled, s.cfg.OAuth2Bypass, scope, s.jwks, s.log, h)
	return wrapped.ServeHTTP
}

// ── JSON helpers ─────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, cause string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"cause": cause})
}

// problemDetails is the TS 29.571 ProblemDetails error body.
type problemDetails struct {
	Title  string `json:"title,omitempty"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Cause  string `json:"cause,omitempty"`
}

// problemJSON writes a spec-compliant application/problem+json error.
func problemJSON(w http.ResponseWriter, code int, cause, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(problemDetails{
		Title:  http.StatusText(code),
		Status: code,
		Detail: detail,
		Cause:  cause,
	})
}
