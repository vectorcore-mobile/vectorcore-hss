package pcf

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/peertracker"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

// Server is the 5G PCF HTTP/2 server.
type Server struct {
	cfg          config.PCFConfig
	store        repository.Repository
	log          *zap.Logger
	jwks         *sbi.JWKSStore
	sbiClient    *sbi.Client
	pt           *peertracker.Tracker
	fpt          *peertracker.Tracker
	assocMu      sync.RWMutex
	associations map[string]*smPolicyAssociation
	assocSeq     atomic.Uint64
	amPolicy     *amPolicyStore
	appMu        sync.Mutex
	appSessions  map[string]*appSession // N5 app session id → session
	appSeq       atomic.Uint64
}

func New(cfg config.PCFConfig, store repository.Repository, log *zap.Logger) *Server {
	if cfg.NFInstanceID == "" {
		cfg.NFInstanceID = sbi.NewNFInstanceID()
		log.Info("pcf: generated NFInstanceID", zap.String("nf_instance_id", cfg.NFInstanceID))
	}
	return &Server{
		cfg:          cfg,
		store:        store,
		log:          log,
		jwks:         &sbi.JWKSStore{},
		sbiClient:    sbi.NewClient(cfg.SBIClient),
		pt:           peertracker.New(),
		fpt:          peertracker.NewWithMaxAge(2 * time.Minute),
		associations: make(map[string]*smPolicyAssociation),
		amPolicy:     newAMPolicyStore(),
		appSessions:  make(map[string]*appSession),
	}
}

func (s *Server) UsePeerTrackers(pt, fpt *peertracker.Tracker) {
	s.pt = pt
	s.fpt = fpt
}

func (s *Server) Config() config.PCFConfig {
	return s.cfg
}

func (s *Server) MountRoutes(r *chi.Mux) {
	s.mountRoutes(r)
}

func (s *Server) StartNRFRegistration() {
	s.startNRFRegistration()
}

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

func (s *Server) Start() error {
	r := chi.NewRouter()
	s.mountRoutes(r)

	addr := net.JoinHostPort(s.cfg.BindAddress, fmt.Sprintf("%d", s.cfg.BindPort))
	s.log.Info("pcf: listening", zap.String("addr", addr))
	go s.startNRFRegistration()

	if s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("pcf: load TLS cert: %w", err)
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

func (s *Server) mountRoutes(r *chi.Mux) {
	s.registerSMPolicyRoutes(r)
	s.registerAMPolicyRoutes(r)
	s.registerPolicyAuthRoutes(r)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","nf":"PCF"}`))
	})
}

func (s *Server) wrapOAuth(scope string, h http.HandlerFunc) http.HandlerFunc {
	wrapped := sbi.OAuthMiddleware(s.cfg.OAuth2Enabled, s.cfg.OAuth2Bypass, scope, s.jwks, s.log, h)
	return wrapped.ServeHTTP
}
