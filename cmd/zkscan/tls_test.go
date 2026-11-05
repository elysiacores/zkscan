package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zkscan/internal/auth"
	"zkscan/internal/puller"
	"zkscan/internal/store"
	"zkscan/internal/zk"
)

func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "zkscan-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return
}

func TestTLSConfigErrors(t *testing.T) {
	if _, usage, err := tlsConfig(options{TLSCert: "a"}); err == nil || !usage {
		t.Fatalf("cert อย่างเดียวต้อง usage error: %v %v", usage, err)
	}
	if _, usage, err := tlsConfig(options{TLSKey: "a"}); err == nil || !usage {
		t.Fatal("key อย่างเดียวต้อง usage error")
	}
	if _, usage, err := tlsConfig(options{TLSCert: "/no/c", TLSKey: "/no/k"}); err == nil || usage {
		t.Fatalf("ไฟล์ไม่มีต้อง error ธรรมดา: %v", err)
	}
	if code, _, e := runCLI("serve", "--tls-cert", "x", "--data", t.TempDir()); code != 2 || !strings.Contains(e, "คู่กัน") {
		t.Fatalf("serve cert อย่างเดียว: %d %q", code, e)
	}
	if code, _, e := runCLI("serve", "--tls-cert", "/no/c", "--tls-key", "/no/k", "--data", t.TempDir()); code != 1 || !strings.Contains(e, "โหลดใบรับรอง") {
		t.Fatalf("serve ไฟล์เสีย: %d %q", code, e)
	}
	t.Setenv("ZKSCAN_TLS_CERT", "/env/c")
	o, _, _ := parseOpts("serve", []string{"--tls-cert", "/flag/c"}, os.Stderr)
	if o.TLSCert != "/flag/c" {
		t.Fatalf("flag ต้องชนะ env: %q", o.TLSCert)
	}
}

func TestServeOverTLSSetsSecureCookie(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)
	cfg, _, err := tlsConfig(options{TLSCert: certPath, TLSKey: keyPath})
	if err != nil || cfg == nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "zkscan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := auth.New(st, auth.Options{})
	client := zk.New()
	app, err := buildApp(appDeps{Store: st, Auth: svc, Puller: puller.New(st, client, nil), Client: client, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pw, _, _, err := bootstrap(context.Background(), svc, dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listen("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(ln)
	defer app.Shutdown()

	pool := x509.NewCertPool()
	pemBytes, _ := os.ReadFile(certPath)
	pool.AppendCertsFromPEM(pemBytes)
	hc := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := "https://" + ln.Addr().String()
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader(url.Values{"username": {"admin"}, "password": {pw}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("login สถานะ %d", resp.StatusCode)
	}
	found := false
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, auth.SessionCookieName+"=") {
			found = true
			if l := strings.ToLower(sc); !strings.Contains(l, "; secure") || !strings.Contains(l, "httponly") {
				t.Fatalf("cookie ต้องมี Secure และ HttpOnly: %s", sc)
			}
		}
	}
	if !found {
		t.Fatal("ไม่พบ Set-Cookie ของ session")
	}
}
