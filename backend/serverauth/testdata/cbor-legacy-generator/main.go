// This isolated module must keep CBOR v2.9.6. The private keys it writes are
// public test material and must never be used outside these fixtures.
package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/fxamacker/cbor/v2"
)

type fixture struct {
	Name                     string `json:"name"`
	PrivateKey               string `json:"test_private_key_pkcs8_b64"`
	CredentialID             string `json:"credential_id_b64"`
	PublicKey                string `json:"public_key_cbor_b64"`
	Attestation              string `json:"attestation_object_b64"`
	AttestationWithExtension string `json:"attestation_with_extension_b64"`
}

type corpus struct {
	CBORVersion string    `json:"generator_cbor_version"`
	RPID        string    `json:"rp_id"`
	Origin      string    `json:"origin"`
	Credentials []fixture `json:"credentials"`
}

func main() {
	write := flag.Bool("write", false, "generate a new corpus; never run during tests")
	path := flag.String("file", "../passkey-cbor-v2.9.6.json", "frozen corpus")
	flag.Parse()
	if err := run(*path, *write); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, write bool) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("missing build info")
	}
	version := ""
	for _, dep := range info.Deps {
		if dep.Path == "github.com/fxamacker/cbor/v2" && dep.Replace == nil {
			version = dep.Version
		}
	}
	if version != "v2.9.6" {
		return fmt.Errorf("fixture encoder must be v2.9.6, got %q", version)
	}
	c := corpus{CBORVersion: version, RPID: "money.example.test", Origin: "https://money.example.test"}
	var original []byte
	if !write {
		var err error
		if original, err = os.ReadFile(path); err != nil {
			return err
		}
		if err = json.Unmarshal(original, &c); err != nil {
			return err
		}
		if c.CBORVersion != version || c.RPID != "money.example.test" || len(c.Credentials) != 3 {
			return fmt.Errorf("unexpected fixture provenance")
		}
	} else {
		for _, name := range []string{"ES256", "RS256", "Ed25519"} {
			var key crypto.Signer
			var err error
			switch name {
			case "ES256":
				key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case "RS256":
				key, err = rsa.GenerateKey(rand.Reader, 2048)
			case "Ed25519":
				_, key, err = ed25519.GenerateKey(rand.Reader)
			}
			if err != nil {
				return err
			}
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				return err
			}
			c.Credentials = append(c.Credentials, fixture{Name: name, PrivateKey: base64.StdEncoding.EncodeToString(der), CredentialID: base64.StdEncoding.EncodeToString([]byte("legacy-cbor-2.9.6-" + name))})
		}
	}
	enc, err := cbor.CTAP2EncOptions().EncMode()
	if err != nil {
		return err
	}
	for i := range c.Credentials {
		f := &c.Credentials[i]
		der, err := base64.StdEncoding.DecodeString(f.PrivateKey)
		if err != nil {
			return err
		}
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return err
		}
		var cose map[int]any
		switch k := key.(type) {
		case *ecdsa.PrivateKey:
			cose = map[int]any{1: 2, 3: -7, -1: 1, -2: k.X.FillBytes(make([]byte, 32)), -3: k.Y.FillBytes(make([]byte, 32))}
		case *rsa.PrivateKey:
			cose = map[int]any{1: 3, 3: -257, -1: k.N.Bytes(), -2: []byte{1, 0, 1}}
		case ed25519.PrivateKey:
			cose = map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(k.Public().(ed25519.PublicKey))}
		default:
			return fmt.Errorf("unexpected key type %T", key)
		}
		public, err := enc.Marshal(cose)
		if err != nil {
			return err
		}
		f.PublicKey = base64.StdEncoding.EncodeToString(public)
		id, err := base64.StdEncoding.DecodeString(f.CredentialID)
		if err != nil {
			return err
		}
		rp := sha256.Sum256([]byte(c.RPID))
		auth := append(append([]byte{}, rp[:]...), 0x45, 0, 0, 0, 0)
		auth = append(auth, make([]byte, 16)...)
		auth = binary.BigEndian.AppendUint16(auth, uint16(len(id)))
		auth = append(append(auth, id...), public...)
		att, err := enc.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
		if err != nil {
			return err
		}
		f.Attestation = base64.StdEncoding.EncodeToString(att)
		extension, err := enc.Marshal(map[string]any{"credProtect": 3})
		if err != nil {
			return err
		}
		auth[32] |= 0x80
		auth = append(auth, extension...)
		att, err = enc.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
		if err != nil {
			return err
		}
		f.AttestationWithExtension = base64.StdEncoding.EncodeToString(att)
	}
	encoded, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if write {
		return os.WriteFile(path, encoded, 0644)
	}
	if !bytes.Equal(encoded, original) {
		return fmt.Errorf("frozen legacy corpus differs from v2.9.6 output")
	}
	fmt.Println("legacy corpus: v2.9.6 provenance and all three key families verified")
	return nil
}
