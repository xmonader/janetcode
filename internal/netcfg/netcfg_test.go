package netcfg

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A TLS server whose certificate no system store knows, as behind a
// TLS-intercepting corporate proxy: refused until its CA file is added,
// then trusted by a plain client on the default transport and by one
// cloned from it, which is how every janetcode client is built.
func TestAddCAFileTrustsAPrivateCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	get := func(c *http.Client) error {
		resp, err := c.Get(srv.URL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	plain := &http.Client{Timeout: 5 * time.Second}
	if err := get(plain); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an unknown CA was trusted before it was added: %v", err)
	}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "corp-ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, block, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := FromEnv(func(k string) string {
		if k == EnvCAFile {
			return caFile
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	if err := get(plain); err != nil {
		t.Fatalf("default transport after adding the CA: %v", err)
	}
	cloned := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 5 * time.Second}
	if err := get(cloned); err != nil {
		t.Fatalf("cloned transport after adding the CA: %v", err)
	}
	if n, err := AddCAFile(caFile); err != nil || n != 0 {
		t.Fatalf("adding the same file twice: n=%d err=%v", n, err)
	}
	if got := Added(); len(got) != 1 || got[0] != caFile {
		t.Fatalf("Added() = %v", got)
	}
}

func TestAddCAFileRefusesAFileWithNoCertificate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-a-cert.pem")
	os.WriteFile(p, []byte("hello"), 0o600)
	if _, err := AddCAFile(p); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("err = %v", err)
	}
	if _, err := AddCAFile(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("a missing file was accepted")
	}
}
