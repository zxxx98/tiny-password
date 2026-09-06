package generator

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"

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

// NewSSHKeyPair generates one key pair. RSA-4096 is gated by a package-wide
// semaphore so concurrent requests cannot exhaust CPU (design §6.5).
func NewSSHKeyPair(algorithm, passphrase, comment string) (*SSHKeyPair, error) {
	switch algorithm {
	case SSHAlgoEd25519:
		return generateEd25519(passphrase, comment)
	case SSHAlgoRSA4096:
		return generateRSA4096(passphrase, comment)
	default:
		return nil, fmt.Errorf("algorithm must be ed25519 or rsa4096")
	}
}

func generateEd25519(passphrase, comment string) (*SSHKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return finishPair(SSHAlgoEd25519, pub, priv, passphrase, comment)
}

func generateRSA4096(passphrase, comment string) (*SSHKeyPair, error) {
	rsaGate <- struct{}{}
	defer func() { <-rsaGate }()
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	return finishPair(SSHAlgoRSA4096, &key.PublicKey, key, passphrase, comment)
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
