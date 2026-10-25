package pcf

import (
	"context"
	"fmt"
	"io"

	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/sbi"
)

func (s *Server) startNRFRegistration() {
	if s.cfg.NRFAddress == "" {
		s.log.Info("pcf: NRF address not configured, skipping registration")
		return
	}

	scheme := sbi.SchemeFromTLS(s.cfg.TLSCertFile)
	ourIP := sbi.ResolveAdvertiseIP(s.cfg.AdvertiseAddress, s.cfg.BindAddress, s.cfg.NRFAddress)
	s.log.Info("pcf: advertising SBI address to NRF", zap.String("ip", ourIP), zap.Int("port", s.cfg.BindPort))
	services := []sbi.NFService{
		sbi.MakeService("npcf-smpolicycontrol", scheme, ourIP, s.cfg.BindPort, []string{"v1"}),
		sbi.MakeService("npcf-am-policy-control", scheme, ourIP, s.cfg.BindPort, []string{"v1"}),
	}

	profile := sbi.NFProfile{
		NFInstanceID:   s.cfg.NFInstanceID,
		NFInstanceName: "vectorcore-pcf",
		NFType:         "PCF",
		NFStatus:       "REGISTERED",
		HeartBeatTimer: sbi.HeartbeatTimer,
		PLMNList:       sbi.PLMNListFromConfig(s.cfg.MCC, s.cfg.MNC),
		IPv4Addresses:  []string{ourIP},
		AllowedNFTypes: []string{"AMF", "SMF", "AF", "UDM", "NRF"},
		NFServices:     services,
	}

	registrar := sbi.NewNRFRegistrar("pcf", s.cfg.NRFAddress, s.cfg.NFInstanceID, profile, s.log, s.Peers(), s.cfg.SBIClient)
	registrar.Start()
	go s.fetchJWKS(registrar.Client())
}

func (s *Server) fetchJWKS(client *sbi.Client) {
	url := fmt.Sprintf("%s/nnrf-nfm/v1/nf-instances?nf-type=NRF", s.cfg.NRFAddress)
	req, err := client.NewRequestWithOptions(context.Background(), "GET", url, nil, sbi.RequestOptions{
		RequesterNFType:       "PCF",
		RequesterNFInstanceID: s.cfg.NFInstanceID,
		TargetNFType:          "NRF",
		TargetServiceName:     "nnrf-nfm",
	})
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", sbi.FormatRequesterUserAgent("PCF", s.cfg.NFInstanceID))
	resp, err := client.Do(req)
	if err != nil {
		s.log.Warn("pcf: JWKS fetch failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.log.Warn("pcf: JWKS fetch returned error", zap.Int("status", resp.StatusCode))
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := s.jwks.Set(raw); err != nil {
			s.log.Warn("pcf: JWKS parse failed", zap.Error(err))
			return
		}
		s.log.Info("pcf: JWKS cached from NRF")
	}
}
