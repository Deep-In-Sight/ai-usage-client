// fakevendor stands in for Anthropic's and OpenAI's hosts in a CI check
// of a release-style build, which has no switch to keep it off the
// network: the check maps those host names to 127.0.0.1 and trusts the
// certificate authority this program makes up at start (written to -ca),
// so every request the agent makes to them ends here and nothing leaves
// the runner. It never forwards a request anywhere.
//
// GET https://chatgpt.com/backend-api/wham/usage answers as -mode says:
// "403" (turned away, as by bot protection) or "nulls" (200 with both
// rate-limit fields null). Every other request gets 404. The plain-HTTP
// -admin address serves GET /healthz, GET /stats (request counts per
// "host method path" and whether they carried an Authorization header;
// never a header's value) and POST /mode?m=403|nulls.
// Test tooling only, never shipped. Every value it sends is fake.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Hosts are the names the certificate covers (the check maps each to
// 127.0.0.1).
var Hosts = []string{"chatgpt.com", "chat.openai.com", "api.openai.com", "auth.openai.com",
	"api.anthropic.com", "console.anthropic.com", "claude.ai"}

const nullsReply = `{"plan_type":"plus","rate_limit":null,"additional_rate_limits":null}`
const forbiddenReply = `{"detail":"Unusual activity has been detected from your device. Try again later."}`

type vendor struct {
	mu     sync.Mutex
	mode   string
	counts map[string]int
	auth   map[string]int
}

func newVendor(mode string) *vendor {
	return &vendor{mode: mode, counts: map[string]int{}, auth: map[string]int{}}
}

// serve is the fake vendors' TLS side.
func (v *vendor) serve(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	key := host + " " + r.Method + " " + r.URL.Path
	v.mu.Lock()
	v.counts[key]++
	if r.Header.Get("Authorization") != "" {
		v.auth[key]++
	}
	mode := v.mode
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if host == "chatgpt.com" && r.Method == http.MethodGet && r.URL.Path == "/backend-api/wham/usage" {
		if mode == "nulls" {
			_, _ = fmt.Fprint(w, nullsReply)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, forbiddenReply)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, `{"error":"fakevendor: not served"}`)
}

// admin is the plain-HTTP control side.
func (v *vendor) admin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": v.mode, "requests": v.counts, "with_authorization": v.auth})
	})
	mux.HandleFunc("POST /mode", func(w http.ResponseWriter, r *http.Request) {
		m := r.URL.Query().Get("m")
		if m != "403" && m != "nulls" {
			http.Error(w, "m must be 403 or nulls", http.StatusBadRequest)
			return
		}
		v.mu.Lock()
		v.mode = m
		v.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// certificates makes a certificate authority and a server certificate
// for Hosts signed by it; caPEM is the authority's certificate.
func certificates() (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "AI Usage CI fake vendor CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		// even trusted by mistake, it can vouch for these names only (and
		// its key is never written anywhere)
		PermittedDNSDomainsCritical: true, PermittedDNSDomains: Hosts}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: Hosts[0]}, DNSNames: Hosts,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:443", "TLS address for the vendors' host names")
	adminAddr := flag.String("admin", "127.0.0.1:18799", "plain-HTTP control address")
	caFile := flag.String("ca", "fakevendor-ca.pem", "where to write the certificate authority (PEM)")
	mode := flag.String("mode", "403", "the usage answer: 403 or nulls")
	flag.Parse()
	cert, caPEM, err := certificates()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caFile, caPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	v := newVendor(*mode)
	go func() { log.Fatal(http.ListenAndServe(*adminAddr, v.admin())) }()
	srv := &http.Server{Addr: *listen, Handler: http.HandlerFunc(v.serve),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	log.Printf("fakevendor on %s (admin %s), CA in %s", *listen, *adminAddr, *caFile)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
