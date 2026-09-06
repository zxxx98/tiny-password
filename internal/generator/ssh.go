package generator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHAlgorithms accepted by the generator (design §6.5): Ed25519 (default)
// and RSA-4096 — no shorter RSA keys.
const (
	SSHAlgoEd25519 = "ed25519"
	SSHAlgoRSA4096 = "rsa4096"
)

// SSHKeyPair is one freshly generated key pair, OpenSSH-encoded. The private
// key is encrypted whenever a passphrase is provided.
type SSHKeyPair struct {
	Algorithm   string
	PublicKey   string // authorized_keys format, one line
	PrivateKey  string // OpenSSH PEM
	Fingerprint string // SHA256:... as ssh-keygen prints
}

// NewSSHKeyPair generates one key pair. It retains the original API while
// applying the RSA generation deadline used by the context-aware API.
func NewSSHKeyPair(algorithm, passphrase, comment string) (*SSHKeyPair, error) {
	return NewSSHKeyPairContext(context.Background(), algorithm, passphrase, comment)
}

// NewSSHKeyPairContext generates one key pair, honoring cancellation while
// waiting for RSA capacity and while the RSA worker is running. The caller's
// passphrase and comment never leave this process or appear in worker argv.
func NewSSHKeyPairContext(ctx context.Context, algorithm, passphrase, comment string) (*SSHKeyPair, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("key generation canceled: %w", err)
	}
	switch algorithm {
	case SSHAlgoEd25519:
		return generateEd25519Context(ctx, passphrase, comment)
	case SSHAlgoRSA4096:
		// A request context often has no server-side deadline. Add a total
		// bound so a worker cannot run indefinitely even in that case.
		generationCtx, cancel := context.WithTimeout(ctx, rsaGenerationTimeout)
		defer cancel()
		return generateRSA4096Context(generationCtx, passphrase, comment)
	default:
		return nil, fmt.Errorf("algorithm must be ed25519 or rsa4096")
	}
}

func generateEd25519(passphrase, comment string) (*SSHKeyPair, error) {
	return generateEd25519Context(context.Background(), passphrase, comment)
}

func generateEd25519Context(ctx context.Context, passphrase, comment string) (*SSHKeyPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return finishPair(SSHAlgoEd25519, pub, priv, passphrase, comment)
}

func generateRSA4096(passphrase, comment string) (*SSHKeyPair, error) {
	return NewSSHKeyPairContext(context.Background(), SSHAlgoRSA4096, passphrase, comment)
}

func generateRSA4096Context(ctx context.Context, passphrase, comment string) (*SSHKeyPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	select {
	case rsaGate <- struct{}{}:
		// Capacity acquired.
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for rsa key worker: %w", ctx.Err())
	}
	defer func() { <-rsaGate }()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	key, err := rsaKeyWorker(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	if key == nil {
		return nil, errors.New("generate rsa key: worker returned no key")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	return finishPair(SSHAlgoRSA4096, &key.PublicKey, key, passphrase, comment)
}

// rsaKeyWorker is a seam for deterministic cancellation tests. The default
// worker is an external process because rsa.GenerateKey cannot be interrupted
// once it starts. It receives no user-controlled or secret arguments.
var rsaKeyWorker = generateRSAKeySubprocess

// rsaGenerationTimeout bounds queue wait, key generation, and worker output
// parsing as one operation. Tests may shorten it through the worker seam.
var rsaGenerationTimeout = 30 * time.Second

const maxRSAWorkerOutput = 2 << 20

const rsaWorkerEnv = "TINY_PASSWORD_RSA_WORKER"

// The RSA worker is the same Go executable launched in a mode that exits
// during package initialization. This keeps the expensive crypto/rand call
// in a killable process without requiring a second installed binary.
func init() {
	if os.Getenv(rsaWorkerEnv) != "1" {
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err == nil {
		var der []byte
		der, err = x509.MarshalPKCS8PrivateKey(key)
		if err == nil && len(der) <= maxRSAWorkerOutput {
			_, err = os.Stdout.Write(der)
		}
	}
	if err != nil {
		// Keep worker diagnostics out of the parent response. The parent maps
		// all worker failures to a generic error.
		os.Exit(1)
	}
	os.Exit(0)
}

// generateRSAKeySubprocess runs the expensive operation out of process so
// exec.CommandContext can kill it on cancellation or deadline. The worker
// emits an unencrypted PKCS#8 DER key; passphrase encryption and OpenSSH
// encoding stay in this process.
func generateRSAKeySubprocess(ctx context.Context) (*rsa.PrivateKey, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("rsa key worker unavailable")
	}
	cmd := exec.CommandContext(ctx, executable)
	// The worker needs only its mode marker; do not inherit application
	// environment variables that might contain secrets.
	cmd.Env = []string{rsaWorkerEnv + "=1"}
	cmd.Stdin = bytes.NewReader(nil)
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if output.exceeded {
		return nil, errors.New("rsa key worker output exceeded limit")
	}
	if runErr != nil {
		return nil, errors.New("rsa key worker failed")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(output.Bytes())
	if err != nil {
		return nil, errors.New("rsa key worker returned invalid key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N == nil || key.N.BitLen() != 4096 {
		return nil, errors.New("rsa key worker returned wrong key type")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("rsa key worker returned invalid key")
	}
	return key, nil
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxRSAWorkerOutput {
		b.exceeded = true
		remaining := maxRSAWorkerOutput - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		return len(p), errors.New("rsa key worker output limit")
	}
	return b.Buffer.Write(p)
}

func finishPair(algorithm string, pub, priv any, passphrase, comment string) (*SSHKeyPair, error) {
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return &SSHKeyPair{
		Algorithm:   algorithm,
		PublicKey:   string(ssh.MarshalAuthorizedKey(sshPub)),
		PrivateKey:  string(pem.EncodeToMemory(block)),
		Fingerprint: ssh.FingerprintSHA256(sshPub),
	}, nil
}

// rsaGate caps concurrent RSA key generation at one; 4096-bit generation is
// CPU-heavy and the generator is interactive.
var rsaGate = make(chan struct{}, 1)
