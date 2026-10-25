package udm

// nrf.go — NRF client: registration, heartbeat, and JWKS fetch.

import (
	"context"
	"fmt"
	"io"

	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

// startNRFRegistration registers the UDM with the NRF and starts the heartbeat.
// Runs in a separate goroutine; retries with backoff if the NRF is unreachable.
func (s *Server) startNRFRegistration() {
	if s.cfg.NRFAddress == "" {
		s.log.Info("udm: NRF address not configured, skipping registration")
		return
	}

	scheme := sbi.SchemeFromTLS(s.cfg.TLSCertFile)
	ourIP := sbi.ResolveAdvertiseIP(s.cfg.AdvertiseAddress, s.cfg.BindAddress, s.cfg.NRFAddress)
	s.log.Info("udm: advertising SBI address to NRF", zap.String("ip", ourIP), zap.Int("port", s.cfg.BindPort))
	services := []sbi.NFService{
		sbi.MakeService("nudm-ueau", scheme, ourIP, s.cfg.BindPort, []string{"v1", "v2"}),
		sbi.MakeService("nudm-sdm", scheme, ourIP, s.cfg.BindPort, []string{"v1", "v2"}),
		sbi.MakeService("nudm-uecm", scheme, ourIP, s.cfg.BindPort, []string{"v1"}),
		sbi.MakeService("nudr-dr", scheme, ourIP, s.cfg.BindPort, []string{"v1", "v2"}),
	}

	profile := sbi.NFProfile{
		NFInstanceID:   s.cfg.NFInstanceID,
		NFInstanceName: "vectorcore-udm-udr",
		NFType:         "UDM",
		NFStatus:       "REGISTERED",
		HeartBeatTimer: sbi.HeartbeatTimer,
		PLMNList:       sbi.PLMNListFromConfig(s.cfg.MCC, s.cfg.MNC),
		IPv4Addresses:  []string{ourIP},
		AllowedNFTypes: []string{"AUSF", "AMF", "SMF", "PCF", "NEF"},
		NFServices:     services,
	}

	registrar := sbi.NewNRFRegistrar("udm", s.cfg.NRFAddress, s.cfg.NFInstanceID, profile, s.log, s.Peers(), s.cfg.SBIClient)
	registrar.Start()
	go s.fetchJWKS(registrar.Client())
}

func (s *Server) fetchJWKS(client *sbi.Client) {
	url := fmt.Sprintf("%s/nnrf-nfm/v1/nf-instances?nf-type=NRF", s.cfg.NRFAddress)
	req, err := client.NewRequestWithOptions(context.Background(), "GET", url, nil, sbi.RequestOptions{
		RequesterNFType:       "UDM",
		RequesterNFInstanceID: s.cfg.NFInstanceID,
		TargetNFType:          "NRF",
		TargetServiceName:     "nnrf-nfm",
	})
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", sbi.FormatRequesterUserAgent("UDM", s.cfg.NFInstanceID))
	resp, err := client.Do(req)
	if err != nil {
		s.log.Warn("udm: JWKS fetch failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.log.Warn("udm: JWKS fetch returned error", zap.Int("status", resp.StatusCode))
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := s.jwks.Set(raw); err != nil {
			s.log.Warn("udm: JWKS parse failed", zap.Error(err))
			return
		}
		s.log.Info("udm: JWKS cached from NRF")
	}
}
