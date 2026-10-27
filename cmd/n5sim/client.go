package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	appSessionsPath = "/npcf-policyauthorization/v1/app-sessions"
	smPoliciesPath  = "/npcf-smpolicycontrol/v1/sm-policies"
)

// client talks HTTP/2 to the PCF (or SCP): h2c with prior knowledge for
// http:// like Kamailio's http_client_request_v2pk, TLS for https://.
type client struct {
	base    string
	http    *http.Client
	verbose bool
}

func newClient(base string, insecure, verbose bool) *client {
	tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}} //nolint:gosec // lab tool, opt-in
	if strings.HasPrefix(base, "http://") {
		tr.AllowHTTP = true
		tr.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second, Transport: tr}, verbose: verbose}
}

type response struct {
	Code     int
	Location string
	Body     []byte
}

// do sends a request with the SCP discovery headers Kamailio adds. target is
// a path on the PCF or an absolute URI (an SM policy Location).
func (c *client) do(method, target, contentType string, body []byte, nfType, service string) (*response, error) {
	url := target
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		url = c.base + target
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("3gpp-sbi-discovery-target-nf-type", nfType)
	req.Header.Set("3gpp-sbi-discovery-service-names", service)
	if c.verbose {
		fmt.Printf(">> %s %s\n%s\n", method, url, body)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := &response{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: bytes.TrimSpace(raw)}
	if c.verbose {
		fmt.Printf("<< %d %s\n%s\n", r.Code, r.Location, raw)
	}
	return r, nil
}

func (c *client) n5(method, target, contentType string, body []byte) (*response, error) {
	return c.do(method, target, contentType, body, "PCF", "npcf-policyauthorization")
}

func (c *client) n7(method, target string, body []byte) (*response, error) {
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	return c.do(method, target, ct, body, "PCF", "npcf-smpolicycontrol")
}

// createAppSession POSTs an AppSessionContext and returns the app session id
// the way Kamailio extracts it: the last path segment of Location.
func (c *client) createAppSession(body []byte) (string, error) {
	r, err := c.n5(http.MethodPost, appSessionsPath, "application/json", body)
	if err != nil {
		return "", err
	}
	if r.Code != http.StatusCreated {
		return "", fmt.Errorf("create app session: got %d, want 201: %s", r.Code, r.Body)
	}
	if r.Location == "" {
		return "", fmt.Errorf("create app session: 201 without Location")
	}
	return path.Base(r.Location), nil
}

func (c *client) patchAppSession(id string, body []byte) error {
	r, err := c.n5(http.MethodPatch, appSessionsPath+"/"+id, "application/merge-patch+json", body)
	if err != nil {
		return err
	}
	if r.Code != http.StatusOK {
		return fmt.Errorf("patch app session %s: got %d, want 200: %s", id, r.Code, r.Body)
	}
	return nil
}

func (c *client) deleteAppSession(id string) error {
	r, err := c.n5(http.MethodPost, appSessionsPath+"/"+id+"/delete", "", nil)
	if err != nil {
		return err
	}
	if r.Code != http.StatusNoContent && r.Code != http.StatusOK {
		return fmt.Errorf("delete app session %s: got %d, want 204: %s", id, r.Code, r.Body)
	}
	return nil
}

func (c *client) createSMPolicy(body []byte) (string, error) {
	r, err := c.n7(http.MethodPost, smPoliciesPath, body)
	if err != nil {
		return "", err
	}
	if r.Code != http.StatusCreated {
		return "", fmt.Errorf("create SM policy: got %d, want 201: %s", r.Code, r.Body)
	}
	if r.Location == "" {
		return "", fmt.Errorf("create SM policy: 201 without Location")
	}
	return r.Location, nil
}

func (c *client) deleteSMPolicy(location string) error {
	r, err := c.n7(http.MethodPost, location+"/delete", []byte("{}"))
	if err != nil {
		return err
	}
	if r.Code != http.StatusNoContent && r.Code != http.StatusOK {
		return fmt.Errorf("delete SM policy: got %d, want 204: %s", r.Code, r.Body)
	}
	return nil
}

// callback is a request the PCF sent to one of our listeners.
type callback struct {
	Path string
	Body []byte
}

// listener is an h2c server standing in for the SMF's SmPolicyUpdateNotify
// endpoint (/smf/...) and the P-CSCF's N5 listener (/af/...). It answers 204
// to everything, as the Open5GS SMF and Kamailio do.
type listener struct {
	srv *http.Server
	smf chan callback
	af  chan callback
}

func startListener(addr string, verbose bool) (*listener, error) {
	l := &listener{smf: make(chan callback, 32), af: make(chan callback, 32)}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cb := callback{Path: r.URL.Path, Body: body}
		if verbose {
			fmt.Printf("<< callback %s %s\n%s\n", r.Method, r.URL.Path, body)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/smf"):
			l.smf <- cb
		case strings.HasPrefix(r.URL.Path, "/af"):
			l.af <- cb
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	l.srv = &http.Server{Handler: h2c.NewHandler(h, &http2.Server{}), ReadHeaderTimeout: 5 * time.Second}
	go l.srv.Serve(ln) //nolint:errcheck
	return l, nil
}

func (l *listener) close() { l.srv.Close() }

func receive(ch <-chan callback, wait time.Duration) (callback, bool) {
	select {
	case cb := <-ch:
		return cb, true
	case <-time.After(wait):
		return callback{}, false
	}
}
