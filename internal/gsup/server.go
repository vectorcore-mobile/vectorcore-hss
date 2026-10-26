package gsup

// server.go -- IPA/GSUP TCP server.
//
// Connection lifecycle per Osmocom IPA protocol:
//   1. Peer connects via TCP.
//   2. HSS sends CCM ID_GET requesting peer identity.
//   3. Peer responds with ID_RESP containing its unit-name; HSS sends ID_ACK.
//   4. Peer sends GSUP requests (AIR, ULR, PUR, ...).
//   5. HSS handles requests and sends responses.
//   6. Keepalive: peer sends CCM PING, HSS responds with PONG.

import (
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/svinson1121/vectorcore-hss/internal/config"
	"github.com/svinson1121/vectorcore-hss/internal/geored"
	"github.com/svinson1121/vectorcore-hss/internal/peertracker"
	"github.com/svinson1121/vectorcore-hss/internal/repository"
)

// defaultLUTimeout bounds the wait for the peer's InsertDataResult during
// an Update Location; osmo-hlr's T-4222 defaults to the same 30 s.
const defaultLUTimeout = 30 * time.Second

// Server is the GSUP/IPA TCP server.
type Server struct {
	cfg       config.GSUPConfig
	store     repository.Repository
	log       *zap.Logger
	pub       geored.TypedPublisher
	pt        *peertracker.Tracker
	luTimeout time.Duration // 0 = defaultLUTimeout
}

// peer is one connected GSUP client (OsmoMSC VLR or OsmoSGSN).
type peer struct {
	conn net.Conn
	// name is the IPA unit-name once ID_RESP arrives, else the remote
	// address. Only the connection's read goroutine touches it.
	name string

	wmu sync.Mutex // serialises writes from the read loop and LU timers

	mu      sync.Mutex
	pending map[string]*pendingLU // Update Locations awaiting InsertDataResult, by IMSI
}

// pendingLU is an Update Location whose ISD has been sent and whose ULA is
// held back until the peer acknowledges the subscriber data.
type pendingLU struct {
	timer *time.Timer
}

func (p *peer) writeGSUP(payload []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return ipaWriteGSUP(p.conn, payload)
}

func (p *peer) writeCCM(msgType byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return ipaWriteCCM(p.conn, msgType)
}

// startLU records a pending Update Location for imsi, replacing any earlier
// one, and arms its timeout. onTimeout runs only if the LU is still pending.
func (p *peer) startLU(imsi string, timeout time.Duration, onTimeout func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old := p.pending[imsi]; old != nil {
		old.timer.Stop()
	}
	lu := &pendingLU{}
	p.pending[imsi] = lu
	lu.timer = time.AfterFunc(timeout, func() {
		if p.takeLU(imsi, lu) {
			onTimeout()
		}
	})
}

// takeLU removes the pending Update Location for imsi and reports whether
// one was pending. When want is non-nil, only that exact LU is removed.
func (p *peer) takeLU(imsi string, want *pendingLU) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	lu := p.pending[imsi]
	if lu == nil || (want != nil && lu != want) {
		return false
	}
	lu.timer.Stop()
	delete(p.pending, imsi)
	return true
}

// stopAll cancels every pending Update Location when the connection closes.
func (p *peer) stopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for imsi, lu := range p.pending {
		lu.timer.Stop()
		delete(p.pending, imsi)
	}
}

// New creates a new GSUP server.
func New(cfg config.GSUPConfig, _ config.HSSConfig, store repository.Repository, log *zap.Logger) *Server {
	return &Server{cfg: cfg, store: store, log: log, pub: geored.NoopTypedPublisher{}, pt: peertracker.New()}
}

// WithGeored attaches a GeoRed publisher to the GSUP server.
func (s *Server) WithGeored(pub geored.TypedPublisher) *Server {
	s.pub = pub
	return s
}

// Peers returns the live GSUP peer tracker.
func (s *Server) Peers() *peertracker.Tracker {
	if s.pt == nil {
		s.pt = peertracker.New()
	}
	return s.pt
}

// Start listens for inbound IPA/GSUP connections.
// Blocks until a fatal listen error; each peer is handled in its own goroutine.
func (s *Server) Start() error {
	addr := net.JoinHostPort(s.cfg.BindAddress, itoa(s.cfg.BindPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.log.Info("gsup: listening", zap.String("addr", addr))

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.log.Error("gsup: accept error", zap.Error(err))
			continue
		}
		go s.handleConn(conn)
	}
}

// handleConn manages the full lifecycle of one peer connection.
func (s *Server) handleConn(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	p := &peer{conn: conn, name: remote, pending: make(map[string]*pendingLU)}
	s.log.Info("gsup: peer connected", zap.String("peer", remote))
	s.Peers().Add(peertracker.Peer{Name: remote, RemoteAddr: remote, Transport: "tcp"})
	defer func() {
		p.stopAll()
		conn.Close()
		s.Peers().Remove(remote)
		s.log.Info("gsup: peer disconnected", zap.String("peer", p.name))
	}()

	// Send CCM ID_GET so the peer identifies itself with its unit-name, as
	// osmo-hlr does on every new GSUP connection.
	p.wmu.Lock()
	_, err := conn.Write(ipaIDGetFrame)
	p.wmu.Unlock()
	if err != nil {
		s.log.Error("gsup: ID_GET write failed", zap.String("peer", remote), zap.Error(err))
		return
	}

	for {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		msg, err := ipaRead(conn)
		if err != nil {
			if !isEOF(err) {
				s.log.Error("gsup: read error", zap.String("peer", p.name), zap.Error(err))
			}
			return
		}

		switch msg.proto {
		case ipaProtoCCM:
			s.handleCCM(p, remote, msg)

		case ipaProtoOSMO:
			if msg.ext != ipaExtGSUP {
				s.log.Debug("gsup: ignoring unknown OSMO ext",
					zap.String("peer", p.name),
					zap.Uint8("ext", msg.ext),
				)
				continue
			}
			gsupMsg, err := Decode(msg.payload)
			if err != nil {
				s.log.Warn("gsup: decode error",
					zap.String("peer", p.name),
					zap.Error(err),
				)
				continue
			}
			s.handleMessage(p, gsupMsg)

		default:
			s.log.Warn("gsup: ignoring non-GSUP IPA frame",
				zap.String("peer", p.name),
				zap.Uint8("proto", msg.proto),
			)
		}
	}
}

// handleCCM processes Common Control Messages (PING, ID_RESP).
func (s *Server) handleCCM(p *peer, remoteAddr string, msg *ipaMsg) {
	if len(msg.payload) == 0 {
		return
	}
	switch msg.payload[0] {
	case ccmMsgPING:
		if err := p.writeCCM(ccmMsgPONG); err != nil {
			s.log.Error("gsup: PONG write failed", zap.String("peer", p.name), zap.Error(err))
		}
	case ccmMsgIDResp:
		if name := parseIDResp(msg.payload); name != "" {
			s.log.Info("gsup: peer identified",
				zap.String("addr", remoteAddr),
				zap.String("name", name),
			)
			s.Peers().Rename(remoteAddr, name)
			p.name = name
		}
		// ID_ACK completes the IPA CCM handshake.
		if err := p.writeCCM(ccmMsgIDACK); err != nil {
			s.log.Error("gsup: ID_ACK write failed", zap.String("peer", p.name), zap.Error(err))
		}
	}
}

func isEOF(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return s == "EOF" ||
		contains(s, "connection reset") ||
		contains(s, "use of closed") ||
		contains(s, "i/o timeout")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 6)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
