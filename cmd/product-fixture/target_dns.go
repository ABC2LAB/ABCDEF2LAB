package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func fixtureInit() error {
	dir := "/target-tls"
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e := os.Chmod(dir, 0700); e != nil {
		return e
	}
	if os.Geteuid() == 0 {
		if e := os.Chown(dir, 1000, 1000); e != nil {
			return e
		}
	}
	write := func(name string, raw []byte) error {
		p := filepath.Join(dir, name)
		if _, e := os.Stat(p); e == nil {
			return fmt.Errorf("fixture_material_exists")
		}
		if e := os.WriteFile(p, raw, 0600); e != nil {
			return e
		}
		if os.Geteuid() == 0 {
			return os.Chown(p, 1000, 1000)
		}
		return nil
	}
	if _, e := os.Stat(filepath.Join(dir, "ca.pem")); e == nil {
		return nil
	}
	now := time.Now()
	until := now.Add(48 * time.Hour)
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "Owned product DNS/TLS fixture CA"}, NotBefore: now.Add(-time.Minute), NotAfter: until, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if e != nil {
		return e
	}
	leafPub, leafKey, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano() + 1), Subject: pkix.Name{CommonName: "Owned Juice Shop fixtures"}, DNSNames: []string{"juice-a.test", "juice-b.test", "juice.test"}, NotBefore: now.Add(-time.Minute), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDer, e := x509.CreateCertificate(rand.Reader, leaf, ca, leafPub, key)
	if e != nil {
		return e
	}
	leafRaw, e := x509.MarshalPKCS8PrivateKey(leafKey)
	if e != nil {
		return e
	}
	token := make([]byte, 32)
	if _, e = rand.Read(token); e != nil {
		return e
	}
	for _, v := range []struct {
		name string
		data []byte
	}{{"leaf.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDer})}, {"key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafRaw})}, {"control-token", []byte(hex.EncodeToString(token))}, {"ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}} {
		if e = write(v.name, v.data); e != nil {
			return e
		}
	}
	return nil
}
func fixtureDNS() error {
	token, e := os.ReadFile("/target-tls/control-token")
	if e != nil {
		return e
	}
	var mu sync.RWMutex
	records := map[string][]string{}
	addresses := map[string]netip.Addr{"juice-a.test.": netip.MustParseAddr("172.31.253.20"), "juice-b.test.": netip.MustParseAddr("172.31.253.22"), "juice.test.": netip.MustParseAddr("172.31.253.20")}
	var queries atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"fixture": true, "dns_queries": queries.Load()})
	})
	mux.HandleFunc("POST /control", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Fixture-Control")), token) != 1 {
			http.Error(w, "forbidden", 403)
			return
		}
		var v struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
			Host   string   `json:"host"`
			IP     string   `json:"ip"`
		}
		d := json.NewDecoder(io.LimitReader(r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&v) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if v.Name != "" {
			name := strings.TrimSuffix(v.Name, ".") + "."
			valid := false
			for host := range addresses {
				valid = valid || name == "_abc2lab-challenge."+host
			}
			if !valid {
				http.Error(w, "invalid", 400)
				return
			}
			records[name] = v.Values
		}
		if v.Host != "" {
			name := strings.TrimSuffix(v.Host, ".") + "."
			ip, e := netip.ParseAddr(v.IP)
			if _, ok := addresses[name]; !ok || e != nil {
				http.Error(w, "invalid", 400)
				return
			}
			addresses[name] = ip
		}
		w.WriteHeader(204)
	})
	go func() { must(http.ListenAndServe(":9083", mux)) }()
	conn, e := net.ListenPacket("udp", ":5353")
	if e != nil {
		return e
	}
	defer conn.Close()
	buffer := make([]byte, 4096)
	for {
		n, peer, e := conn.ReadFrom(buffer)
		if e != nil {
			return e
		}
		var req dnsmessage.Message
		if req.Unpack(buffer[:n]) != nil {
			continue
		}
		queries.Add(1)
		response := dnsmessage.Message{Header: dnsmessage.Header{ID: req.ID, Response: true, Authoritative: true, RecursionDesired: req.RecursionDesired, RecursionAvailable: true}, Questions: req.Questions}
		mu.RLock()
		for _, q := range req.Questions {
			if q.Type == dnsmessage.TypeTXT {
				for _, txt := range records[q.Name.String()] {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.TXTResource{TXT: []string{txt}}})
				}
			}
			if q.Type == dnsmessage.TypeA {
				if ip, ok := addresses[q.Name.String()]; ok && ip.Is4() {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: ip.As4()}})
				}
			}
		}
		mu.RUnlock()
		raw, e := response.Pack()
		if e == nil {
			_, _ = conn.WriteTo(raw, peer)
		}
	}
}
