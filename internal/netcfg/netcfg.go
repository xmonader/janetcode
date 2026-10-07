// Package netcfg holds the process-wide settings every outbound HTTP
// client shares: the extra certificate authorities a TLS-intercepting
// proxy or a private gateway needs.
//
// Every client janetcode makes (the providers, fetch_url, web search,
// remote MCP and its OAuth, --update, the decisions service, the price
// table) either uses http.DefaultTransport or clones it, so the roots are
// added there once, at startup, before any of them is built. A clone
// made after the first file was added shares the pool, so a second file
// added later is seen by it too; one made before sees none of them.
// Proxies need nothing here: DefaultTransport already reads HTTP_PROXY,
// HTTPS_PROXY and NO_PROXY, and so does every clone of it.
package netcfg

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	mu    sync.Mutex
	pool  *x509.CertPool
	added = map[string]bool{}
)

// EnvCAFile names a PEM file of extra certificate authorities, one or
// more certificates, trusted beside the system's own.
const EnvCAFile = "JANETCODE_CA_FILE"

// AddCAFile trusts the certificates in the PEM file at path for every
// outbound HTTPS connection, in addition to the system roots. Adding the
// same file twice is a no-op. It returns how many certificates it added.
func AddCAFile(path string) (int, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("CA file: %w", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if added[path] {
		return 0, nil
	}
	n, err := appendPEM(data)
	if err != nil {
		return 0, fmt.Errorf("CA file %s: %w", path, err)
	}
	added[path] = true
	return n, nil
}

// addCADir trusts every certificate file in dir, as SSL_CERT_DIR names
// them: files ending .pem, .crt or .cer (the hashed .0 links too).
func addCADir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("CA directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".pem" && ext != ".crt" && ext != ".cer" && ext != ".0" {
			continue
		}
		// A directory of certificates often holds a stray file that is
		// not one; skip it rather than refuse the whole directory.
		_, _ = AddCAFile(filepath.Join(dir, name))
	}
	return nil
}

// appendPEM adds the PEM certificates in data to the shared pool and
// points DefaultTransport at it. Callers hold mu.
func appendPEM(data []byte) (int, error) {
	// AppendCertsFromPEM reports only whether it found one; the count is
	// for the doctor's line.
	certs := strings.Count(string(data), "-----BEGIN CERTIFICATE-----")
	if certs == 0 || !x509.NewCertPool().AppendCertsFromPEM(data) {
		return 0, errors.New("no PEM certificate in it")
	}
	if pool == nil {
		sys, err := x509.SystemCertPool()
		if err != nil || sys == nil {
			sys = x509.NewCertPool()
		}
		pool = sys
	}
	pool.AppendCertsFromPEM(data)
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return certs, errors.New("the default HTTP transport has been replaced; cannot add roots to it")
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr.TLSClientConfig.RootCAs = pool
	return certs, nil
}

// FromEnv adds the certificate authorities the environment names:
// JANETCODE_CA_FILE everywhere, and SSL_CERT_FILE and SSL_CERT_DIR on
// macOS and Windows, where Go verifies against the system store and would
// otherwise ignore the two variables every other tool honours. On Linux
// and the BSDs Go already reads them itself.
func FromEnv(getenv func(string) string) error {
	var errs []error
	if f := getenv(EnvCAFile); f != "" {
		if _, err := AddCAFile(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvCAFile, err))
		}
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if f := getenv("SSL_CERT_FILE"); f != "" {
			if _, err := AddCAFile(f); err != nil {
				errs = append(errs, fmt.Errorf("SSL_CERT_FILE: %w", err))
			}
		}
		for _, d := range filepath.SplitList(getenv("SSL_CERT_DIR")) {
			if d != "" {
				if err := addCADir(d); err != nil {
					errs = append(errs, fmt.Errorf("SSL_CERT_DIR: %w", err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Added lists the CA files trusted so far, for --doctor.
func Added() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(added))
	for p := range added {
		out = append(out, p)
	}
	return out
}
