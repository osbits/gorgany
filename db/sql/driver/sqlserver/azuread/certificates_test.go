package azuread

import (
	"bytes"
	"crypto"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha1"   // registers crypto.SHA1
	_ "crypto/sha256" // registers crypto.SHA256
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The service principal's certificates, made in the test that uses them. None is committed: a
// .pem or .pfx in the tree reads as a leaked key to every scanner, and the image's ignore file
// excludes them anyway.

// testCertificate is a self-signed certificate and its RSA key, the only kind of key MSAL signs
// a client assertion with.
type testCertificate struct {
	der []byte
	key *rsa.PrivateKey
}

func newTestCertificate(t *testing.T) testCertificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gorgany test service principal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return testCertificate{der: der, key: key}
}

// pem is the certificate and its unencrypted PKCS#8 key as one PEM file, the form `az ad sp
// create-for-rbac --create-cert` writes.
func (c testCertificate) pem(t *testing.T) []byte {
	t.Helper()

	keyDER, err := x509.MarshalPKCS8PrivateKey(c.key)
	require.NoError(t, err)
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}

// writeFile writes data to a file of its own in t's temporary directory, and returns its path.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// The PKCS#12 encoding, RFC 7292, in the one shape golang.org/x/crypto/pkcs12 reads, which is
// what azidentity.ParseCertificates reads a .pfx with: an authenticated safe of two unencrypted
// safe contents, one with the certificate and one with the key shrouded under
// pbeWithSHAAnd3-KeyTripleDES-CBC, and an HMAC-SHA1 over it. It is the legacy profile Windows and
// OpenSSL's -legacy export, and nothing here needs a module the framework does not already link.
// pkcs12WithMAC can put an HMAC-SHA256 over it instead, the MAC OpenSSL 3 and current Windows
// export by default, which x/crypto refuses.

var (
	oidData           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidCertBag        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidShroudedKeyBag = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 2}
	oidX509Cert       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidPBEWithSHA3DES = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 3}
	oidSHA1           = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256         = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
)

type p12ContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue
}

type p12SafeBag struct {
	ID    asn1.ObjectIdentifier
	Value asn1.RawValue
}

type p12CertBag struct {
	ID   asn1.ObjectIdentifier
	Data asn1.RawValue
}

type p12PBEParams struct {
	Salt       []byte
	Iterations int
}

type p12EncryptedKey struct {
	Algorithm pkix.AlgorithmIdentifier
	Data      []byte
}

type p12DigestInfo struct {
	Algorithm pkix.AlgorithmIdentifier
	Digest    []byte
}

type p12MacData struct {
	Mac        p12DigestInfo
	Salt       []byte
	Iterations int
}

type p12PFX struct {
	Version  int
	AuthSafe p12ContentInfo
	MacData  p12MacData
}

const p12Iterations = 1000

// pkcs12 is the certificate and its key as a PKCS#12 file that password opens.
func (c testCertificate) pkcs12(t *testing.T, password string) []byte {
	t.Helper()
	return c.pkcs12WithMAC(t, password, crypto.SHA1)
}

// pkcs12WithMAC is pkcs12 with its MAC computed over macHash, SHA-1 or SHA-256.
func (c testCertificate) pkcs12WithMAC(t *testing.T, password string, macHash crypto.Hash) []byte {
	t.Helper()
	macOID := map[crypto.Hash]asn1.ObjectIdentifier{crypto.SHA1: oidSHA1, crypto.SHA256: oidSHA256}[macHash]
	require.NotNil(t, macOID, "a SHA-1 or SHA-256 MAC")

	secret := bmp(password)
	mustDER := func(v any) []byte {
		t.Helper()
		der, err := asn1.Marshal(v)
		require.NoError(t, err)
		return der
	}
	// explicit wraps der in the [0] EXPLICIT tag every PKCS#12 container puts its content under.
	explicit := func(der []byte) asn1.RawValue {
		return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: der}
	}
	salt := func() []byte {
		s := make([]byte, 8)
		_, err := rand.Read(s)
		require.NoError(t, err)
		return s
	}

	certBag := p12SafeBag{ID: oidCertBag, Value: explicit(mustDER(p12CertBag{
		ID: oidX509Cert, Data: explicit(mustDER(c.der)),
	}))}

	keyDER, err := x509.MarshalPKCS8PrivateKey(c.key)
	require.NoError(t, err)
	keySalt := salt()
	block, err := des.NewTripleDESCipher(pkcs12KDF(crypto.SHA1, keySalt, secret, p12Iterations, 1, 24))
	require.NoError(t, err)
	padded := pkcs7Pad(keyDER, block.BlockSize())
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, pkcs12KDF(crypto.SHA1, keySalt, secret, p12Iterations, 2, 8)).CryptBlocks(encrypted, padded)
	keyBag := p12SafeBag{ID: oidShroudedKeyBag, Value: explicit(mustDER(p12EncryptedKey{
		Algorithm: pkix.AlgorithmIdentifier{
			Algorithm:  oidPBEWithSHA3DES,
			Parameters: asn1.RawValue{FullBytes: mustDER(p12PBEParams{Salt: keySalt, Iterations: p12Iterations})},
		},
		Data: encrypted,
	}))}

	safe := func(bag p12SafeBag) p12ContentInfo {
		return p12ContentInfo{ContentType: oidData, Content: explicit(mustDER(mustDER([]p12SafeBag{bag})))}
	}
	authenticatedSafe := mustDER([]p12ContentInfo{safe(certBag), safe(keyBag)})

	macSalt := salt()
	mac := hmac.New(macHash.New, pkcs12KDF(macHash, macSalt, secret, p12Iterations, 3, macHash.Size()))
	mac.Write(authenticatedSafe)

	return mustDER(p12PFX{
		Version:  3,
		AuthSafe: p12ContentInfo{ContentType: oidData, Content: explicit(mustDER(authenticatedSafe))},
		MacData: p12MacData{
			Mac:        p12DigestInfo{Algorithm: pkix.AlgorithmIdentifier{Algorithm: macOID}, Digest: mac.Sum(nil)},
			Salt:       macSalt,
			Iterations: p12Iterations,
		},
	})
}

// bmp is s as a PKCS#12 password: UCS-2, big-endian, with a zero terminator.
func bmp(s string) []byte {
	out := make([]byte, 0, 2*len(s)+2)
	for _, r := range s {
		out = append(out, byte(r>>8), byte(r))
	}
	return append(out, 0, 0)
}

func pkcs7Pad(data []byte, size int) []byte {
	n := size - len(data)%size
	return append(append([]byte{}, data...), bytes.Repeat([]byte{byte(n)}, n)...)
}

// pkcs12KDF is RFC 7292's key derivation, appendix B.2, over h, SHA-1 or SHA-256, whose blocks
// are both 64 bytes: size bytes of key material for purpose id (1 a key, 2 an IV, 3 a MAC key).
func pkcs12KDF(h crypto.Hash, salt, password []byte, iterations int, id byte, size int) []byte {
	u, v := h.Size(), 64
	sum := func(data []byte) []byte {
		hash := h.New()
		hash.Write(data)
		return hash.Sum(nil)
	}
	fill := func(pattern []byte) []byte {
		if len(pattern) == 0 {
			return nil
		}
		out := make([]byte, v*((len(pattern)+v-1)/v))
		for i := range out {
			out[i] = pattern[i%len(pattern)]
		}
		return out
	}

	d := bytes.Repeat([]byte{id}, v)
	i := append(fill(salt), fill(password)...)
	var a []byte
	for len(a) < size {
		digest := sum(append(append([]byte{}, d...), i...))
		for n := 1; n < iterations; n++ {
			digest = sum(digest)
		}
		a = append(a, digest[:u]...)

		bPlusOne := new(big.Int).Add(new(big.Int).SetBytes(fill(digest)), big.NewInt(1))
		for j := 0; j < len(i); j += v {
			added := new(big.Int).Add(new(big.Int).SetBytes(i[j:j+v]), bPlusOne).Bytes()
			if len(added) > v {
				added = added[len(added)-v:]
			}
			block := make([]byte, v)
			copy(block[v-len(added):], added)
			copy(i[j:j+v], block)
		}
	}
	return a[:size]
}
