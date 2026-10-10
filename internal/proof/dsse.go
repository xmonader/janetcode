package proof

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/xdg"
)

// Envelope is a DSSE envelope (github.com/secure-systems-lab/dsse): the
// statement's JSON, base64-encoded, and the signatures over its
// pre-authentication encoding (PAE).
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one DSSE signature. KeyID names the key ("ed25519:" and
// the first 16 hex digits of the public key's sha256).
type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// Signer signs a DSSE pre-authentication encoding. KeySigner is the one
// janetcode ships: a local ed25519 key, standard library only, no
// network. It is the hook for keyless signing later: a Sigstore signer
// (an OIDC token traded at Fulcio for a short-lived certificate, the
// signature logged at Rekor) implements the same two methods, and its
// certificate and log entry would travel as extra fields on Signature,
// which DSSE allows. Verification would then trust an identity rather
// than a key id.
type Signer interface {
	KeyID() (string, error)
	Sign(pae []byte) ([]byte, error)
}

// pae is DSSE's pre-authentication encoding: what is actually signed, so
// a signature over one payload type cannot be replayed as another.
func pae(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

// SignStatement marshals s and wraps it in an envelope signed by signer.
func SignStatement(s Statement, signer Signer) (Envelope, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return Envelope{}, err
	}
	id, err := signer.KeyID()
	if err != nil {
		return Envelope{}, err
	}
	sig, err := signer.Sign(pae(PayloadType, payload))
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		PayloadType: PayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: id, Sig: base64.StdEncoding.EncodeToString(sig)}},
	}, nil
}

// Statement decodes the envelope's payload, without checking any
// signature: CheckSignature is the separate step that says whether to
// believe it.
func (e Envelope) Statement() (Statement, error) {
	if e.PayloadType != PayloadType {
		return Statement{}, fmt.Errorf("payload type %q, want %q", e.PayloadType, PayloadType)
	}
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return Statement{}, fmt.Errorf("payload is not base64: %w", err)
	}
	var s Statement
	if err := json.Unmarshal(raw, &s); err != nil {
		return Statement{}, fmt.Errorf("payload is not a statement: %w", err)
	}
	if s.Type != StatementType || s.PredicateType != PredicateType {
		return Statement{}, fmt.Errorf("statement is %s/%s, not janetcode change evidence", s.Type, s.PredicateType)
	}
	return s, nil
}

// SigStatus is what CheckSignature concluded.
type SigStatus string

const (
	// SigValid: signed by a trusted key, and the signature holds.
	SigValid SigStatus = "valid"
	// SigUntrusted: no signature is from a key this machine trusts, so
	// whether it holds cannot be said.
	SigUntrusted SigStatus = "untrusted key"
	// SigInvalid: a trusted key's signature does not match the payload —
	// the evidence was changed after it was signed.
	SigInvalid SigStatus = "invalid"
	// SigNone: there is no evidence envelope at all.
	SigNone SigStatus = "none"
)

// CheckSignature checks e's signatures against trusted (key id -> public
// key). It reports the first trusted key's verdict, and the key id it
// is about.
func CheckSignature(e Envelope, trusted map[string]ed25519.PublicKey) (SigStatus, string) {
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return SigInvalid, ""
	}
	msg := pae(e.PayloadType, raw)
	var untrusted string
	for _, s := range e.Signatures {
		pub, ok := trusted[s.KeyID]
		if !ok {
			untrusted = s.KeyID
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil || !ed25519.Verify(pub, msg, sig) {
			return SigInvalid, s.KeyID
		}
		return SigValid, s.KeyID
	}
	return SigUntrusted, untrusted
}

// KeyIDOf is the key id of an ed25519 public key.
func KeyIDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// Key file names under the key directory (DefaultKeyDir).
const (
	keyFile     = "ed25519.key"
	trustedFile = "trusted"
)

// DefaultKeyDir is where the local signing key and the trusted keys list
// live: <config dir>/evidence, beside config.json.
func DefaultKeyDir() (string, error) {
	dir, err := xdg.ConfigDir(os.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "evidence"), nil
}

// KeySigner signs with the ed25519 key in Dir, creating it (mode 0600) on
// first use. Loading is deferred to the first Sign or KeyID, so a session
// that never commits never touches the key.
type KeySigner struct {
	Dir string

	once sync.Once
	key  ed25519.PrivateKey
	err  error
}

// NewKeySigner returns a signer for the key in dir.
func NewKeySigner(dir string) *KeySigner { return &KeySigner{Dir: dir} }

func (k *KeySigner) load() (ed25519.PrivateKey, error) {
	k.once.Do(func() { k.key, k.err = loadOrCreateKey(k.Dir) })
	return k.key, k.err
}

// KeyID implements Signer.
func (k *KeySigner) KeyID() (string, error) {
	key, err := k.load()
	if err != nil {
		return "", err
	}
	return KeyIDOf(key.Public().(ed25519.PublicKey)), nil
}

// Sign implements Signer.
func (k *KeySigner) Sign(msg []byte) ([]byte, error) {
	key, err := k.load()
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(key, msg), nil
}

// PublicKey is the signer's public key.
func (k *KeySigner) PublicKey() (ed25519.PublicKey, error) {
	key, err := k.load()
	if err != nil {
		return nil, err
	}
	return key.Public().(ed25519.PublicKey), nil
}

// TrustLine is the public key as one line of a trusted file: "<key id>
// <base64 public key>". Appending it to another machine's
// <config>/evidence/trusted lets that machine verify this one's evidence.
func TrustLine(pub ed25519.PublicKey) string {
	return KeyIDOf(pub) + " " + base64.StdEncoding.EncodeToString(pub)
}

func loadOrCreateKey(dir string) (ed25519.PrivateKey, error) {
	if dir == "" {
		return nil, errors.New("no evidence key directory")
	}
	path := filepath.Join(dir, keyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		return parseKey(data, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := fsutil.AtomicWriteMode(path, pemData, 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

func parseKey(data []byte, path string) (ed25519.PrivateKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, fmt.Errorf("%s: not a PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return priv, nil
}

// TrustedKeys is the set verify believes: the local key in dir (when it
// exists — verify never creates one) and every "<key id> <base64 key>"
// line of dir/trusted. A line whose id does not match its key is skipped:
// the id is derived, so a mismatch is a typo or a forgery.
func TrustedKeys(dir string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	if dir == "" {
		return out, nil
	}
	if data, err := os.ReadFile(filepath.Join(dir, keyFile)); err == nil {
		priv, err := parseKey(data, filepath.Join(dir, keyFile))
		if err != nil {
			return nil, err
		}
		pub := priv.Public().(ed25519.PublicKey)
		out[KeyIDOf(pub)] = pub
	}
	f, err := os.Open(filepath.Join(dir, trustedFile))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		pub := ed25519.PublicKey(raw)
		if KeyIDOf(pub) != fields[0] {
			continue
		}
		out[fields[0]] = pub
	}
	return out, sc.Err()
}

// AttachEvidence signs s and stores it as the evidence note on commit,
// merging with an envelope already there (another session's evidence for
// the same commit). The merged statement is re-signed by signer: the
// envelope always carries exactly one signature, over everything in it.
func AttachEvidence(ctx context.Context, repoDir, commit string, s Statement, signer Signer) (Envelope, error) {
	if err := EnsureRewriteConfig(ctx, repoDir); err != nil {
		return Envelope{}, err
	}
	if existing, ok, err := ReadEvidence(ctx, repoDir, commit); err == nil && ok {
		if prev, err := existing.Statement(); err == nil && prev.subjectTree() == s.subjectTree() {
			s = mergeEvidence(prev, s)
		}
	}
	env, err := SignStatement(s, signer)
	if err != nil {
		return Envelope{}, err
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return Envelope{}, err
	}
	if _, err := gitx.RunInput(ctx, repoDir, data, "notes", "--ref="+EvidenceRef, "add", "-f", "-F", "-", commit); err != nil {
		return Envelope{}, gitx.Wrapf(err, "git notes add")
	}
	return env, nil
}

// ReadEvidence returns the evidence envelope attached to commit, ok=false
// when it has none.
func ReadEvidence(ctx context.Context, repoDir, commit string) (Envelope, bool, error) {
	out, err := gitx.RunBytes(ctx, repoDir, nil, "notes", "--ref="+EvidenceRef, "show", commit)
	if err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) && noNoteRe.MatchString(ge.Stderr) {
			return Envelope{}, false, nil
		}
		return Envelope{}, false, gitx.Wrapf(err, "git notes show")
	}
	var e Envelope
	if err := json.Unmarshal(out, &e); err != nil {
		return Envelope{}, false, fmt.Errorf("evidence note on %s is not valid JSON: %w", gitx.Short(commit), err)
	}
	return e, true, nil
}

// TreeOf is the tree sha of commit.
func TreeOf(ctx context.Context, repoDir, commit string) (string, error) {
	return gitx.Run(ctx, repoDir, nil, "rev-parse", "--verify", "-q", commit+"^{tree}")
}
