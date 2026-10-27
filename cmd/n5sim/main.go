// Command n5sim is an N5 (Npcf_PolicyAuthorization) test client for the
// VectorCore PCF. It sends the requests the docker_open5gs Kamailio P-CSCF
// sends with WITH_N5, so VoNR policy can be tested without a P-CSCF, and it
// can stand in for the SMF so it can be tested without a 5GC as well.
//
// Usage:
//
//	n5sim [global-flags] <command> [command-flags]
//
//	n5sim suite    --supi imsi-001010000000001            # fake SMF + AF, full pass/fail run
//	n5sim smf      --supi imsi-001010000000001 --ue-ip 10.46.0.26
//	n5sim register --ue-ip 10.46.0.26
//	n5sim call     --ue-ip 10.46.0.26 --peer-ip 10.46.0.27 --hold 20s
//	n5sim get      --id 3
//	n5sim delete   --id 3
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type globals struct {
	pcf      string
	insecure bool
	verbose  bool
}

func main() {
	var g globals
	flag.StringVar(&g.pcf, "pcf", "http://127.0.0.1:7777", "PCF (or SCP) base URI; http:// is h2c prior knowledge like Kamailio")
	flag.BoolVar(&g.insecure, "insecure", false, "skip TLS certificate verification for https://")
	flag.BoolVar(&g.verbose, "v", false, "print every request, response and callback")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		usage()
		os.Exit(1)
	}
	c := newClient(g.pcf, g.insecure, g.verbose)

	var err error
	switch args[0] {
	case "suite":
		err = runSuite(c, args[1:])
	case "smf":
		err = runSMF(c, args[1:])
	case "register":
		err = runRegister(c, args[1:])
	case "call":
		err = runCall(c, args[1:])
	case "get":
		err = runGet(c, args[1:])
	case "delete":
		err = runDelete(c, args[1:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `n5sim — N5 Npcf_PolicyAuthorization test client (Kamailio P-CSCF requests)

Usage: n5sim [global-flags] <command> [command-flags]

Commands:
  suite     run register/call/re-INVITE/BYE/video/PDU-release against the PCF,
            with n5sim playing both the SMF and the P-CSCF; prints PASS/FAIL
  smf       create an SM policy for a UE as the SMF would, print the
            SmPolicyUpdateNotify the PCF sends, delete it on Ctrl-C
  register  send the REGISTER body (CONTROL / AF_SIGNALLING)
  call      send a call body, optionally re-INVITE, hold, then BYE
  get       GET an app session
  delete    delete an app session (BYE)

Global flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, "\nRun 'n5sim <command> -h' for command flags.\n")
}

// common holds the flags shared by the AF commands.
type common struct {
	ueIP, dnn, supi, gpsi, pcscf, notifURI string
}

func (cm *common) register(fs *flag.FlagSet, defUE string) {
	fs.StringVar(&cm.ueIP, "ue-ip", defUE, "UE IMS PDU session IPv4 address (ueIpv4)")
	fs.StringVar(&cm.dnn, "dnn", "ims", "DNN")
	fs.StringVar(&cm.supi, "supi", "imsi-001010000000001", "SUPI (a subscriber provisioned with the DNN)")
	fs.StringVar(&cm.gpsi, "gpsi", "msisdn-0012345601", "GPSI sent on calls (Kamailio sends the caller's on MT too)")
	fs.StringVar(&cm.pcscf, "pcscf-ip", "10.46.0.1", "P-CSCF address used in the REGISTER flows")
	fs.StringVar(&cm.notifURI, "notif-uri", "http://10.46.0.1:7777", "notifUri the PCF sends terminate notifications to")
}

func (cm *common) params() ascParams {
	return ascParams{UE: cm.ueIP, DNN: cm.dnn, SUPI: cm.supi, GPSI: cm.gpsi, PCSCF: cm.pcscf, NotifURI: cm.notifURI}
}

func runRegister(c *client, args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	var cm common
	cm.register(fs, "10.46.0.26")
	fs.Parse(args) //nolint:errcheck

	id, err := c.createAppSession(registerBody(cm.params()))
	if err != nil {
		return err
	}
	fmt.Printf("registered: app session %s (delete with: n5sim delete --id %s)\n", id, id)
	return nil
}

type callFlags struct {
	common
	peerIP   string
	uePort   int
	peerPort int
	bw       int
	video    bool
	reinvite bool
	hold     time.Duration
	keep     bool
}

func (cf *callFlags) register(fs *flag.FlagSet, defUE, defPeer string) {
	cf.common.register(fs, defUE)
	fs.StringVar(&cf.peerIP, "peer-ip", defPeer, "remote party's RTP address")
	fs.IntVar(&cf.uePort, "ue-port", 50000, "UE RTP port (RTCP is +1, video +2/+3)")
	fs.IntVar(&cf.peerPort, "peer-port", 40000, "remote RTP port (RTCP is +1, video +2/+3)")
	fs.IntVar(&cf.bw, "bw", 38, "b=AS bandwidth in Kbps; 0 is what Kamailio sends when the SDP has none")
	fs.BoolVar(&cf.video, "video", false, "add a video component")
}

func (cf *callFlags) params() ascParams {
	p := cf.common.params()
	p.UEPort, p.Peer, p.BandwidthKbps, p.Video = cf.uePort, party{IP: cf.peerIP, Port: cf.peerPort}, cf.bw, cf.video
	return p
}

func runCall(c *client, args []string) error {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	var cf callFlags
	cf.register(fs, "10.46.0.26", "10.46.0.27")
	fs.BoolVar(&cf.reinvite, "reinvite", false, "PATCH a re-INVITE moving the remote port by +10 after the INVITE")
	fs.DurationVar(&cf.hold, "hold", 10*time.Second, "how long the call lasts before the BYE (Ctrl-C hangs up early)")
	fs.BoolVar(&cf.keep, "keep", false, "leave the app session up (no BYE)")
	fs.Parse(args) //nolint:errcheck

	p := cf.params()
	id, err := c.createAppSession(callBody(p))
	if err != nil {
		return err
	}
	fmt.Printf("INVITE: app session %s\n", id)
	if cf.reinvite {
		p.Peer.Port += 10
		if err := c.patchAppSession(id, callBody(p)); err != nil {
			return err
		}
		fmt.Printf("re-INVITE: app session %s updated (remote port %d)\n", id, p.Peer.Port)
	}
	if cf.keep {
		fmt.Printf("kept up (delete with: n5sim delete --id %s)\n", id)
		return nil
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-time.After(cf.hold):
	case <-sig:
	}
	if err := c.deleteAppSession(id); err != nil {
		return err
	}
	fmt.Printf("BYE: app session %s deleted\n", id)
	return nil
}

func runGet(c *client, args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	id := fs.String("id", "", "app session id (required)")
	fs.Parse(args) //nolint:errcheck
	if *id == "" {
		return errors.New("--id is required")
	}
	r, err := c.n5(http.MethodGet, appSessionsPath+"/"+*id, "", nil)
	if err != nil {
		return err
	}
	fmt.Printf("%d\n%s\n", r.Code, r.Body)
	if r.Code != http.StatusOK {
		return fmt.Errorf("got %d", r.Code)
	}
	return nil
}

func runDelete(c *client, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	id := fs.String("id", "", "app session id (required)")
	fs.Parse(args) //nolint:errcheck
	if *id == "" {
		return errors.New("--id is required")
	}
	if err := c.deleteAppSession(*id); err != nil {
		return err
	}
	fmt.Printf("app session %s deleted\n", *id)
	return nil
}

func runSMF(c *client, args []string) error {
	fs := flag.NewFlagSet("smf", flag.ExitOnError)
	var cm common
	cm.register(fs, "10.46.0.26")
	listen := fs.String("listen", ":9090", "address the fake SMF listens on for SmPolicyUpdateNotify")
	advertise := fs.String("advertise", "", "base URI the PCF reaches the listener at (default: this host's address towards the PCF)")
	fs.Parse(args) //nolint:errcheck

	l, base, err := listenAndAdvertise(c, *listen, *advertise)
	if err != nil {
		return err
	}
	defer l.close()

	loc, err := c.createSMPolicy(smPolicyBody(cm.supi, cm.dnn, cm.ueIP, base+"/smf"))
	if err != nil {
		return err
	}
	fmt.Printf("SM policy %s for %s (%s), notifications to %s/smf — Ctrl-C to release\n", loc, cm.ueIP, cm.dnn, base)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case cb := <-l.smf:
			fmt.Printf("SMF notify %s\n%s\n", cb.Path, indent(cb.Body))
		case <-sig:
			if err := c.deleteSMPolicy(loc); err != nil {
				return err
			}
			fmt.Println("SM policy deleted (PDU session released)")
			return nil
		}
	}
}

// listenAndAdvertise starts the callback listener and works out the base URI
// the PCF should use to reach it.
func listenAndAdvertise(c *client, listen, advertise string) (*listener, string, error) {
	l, err := startListener(listen, c.verbose)
	if err != nil {
		return nil, "", err
	}
	if advertise != "" {
		return l, advertise, nil
	}
	_, port, _ := net.SplitHostPort(listen)
	ip, err := localIPTowards(c.base)
	if err != nil {
		l.close()
		return nil, "", fmt.Errorf("cannot work out --advertise: %w", err)
	}
	return l, "http://" + net.JoinHostPort(ip, port), nil
}

// localIPTowards returns the local address the kernel would use to reach
// the host in base (no packet is sent).
func localIPTowards(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.Dial("udp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func indent(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	return string(mustJSON(v))
}
