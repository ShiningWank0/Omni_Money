// Command omni-totp creates an independent TOTP seed for Omni Money.
//
// The seed is intentionally generated outside the server process so the
// Pangolin gateway and Omni Money use separate second factors. The output
// file is created atomically with O_EXCL and is never overwritten.
package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/authn"
)

const generatedSecretSize = 20

func main() {
	out := flag.String("out", "", "new file in which to store the Base32 TOTP seed (required)")
	issuer := flag.String("issuer", "Omni Money", "issuer shown by the authenticator app")
	account := flag.String("account", "admin", "account label shown by the authenticator app")
	flag.Parse()
	outPath := strings.TrimSpace(*out)

	err := validateNewSecretPath(outPath)
	secret := []byte(nil)
	if err == nil {
		secret, err = generateTOTPSecret()
	}
	if err == nil {
		seed, encodeErr := authn.EncodeTOTPSecret(secret)
		if encodeErr != nil {
			err = encodeErr
		} else {
			uri, uriErr := buildOTPAuthURI(*issuer, *account, seed)
			if uriErr != nil {
				err = uriErr
			} else {
				// These values are secrets by design. Display them only during the
				// explicit one-time setup command; never include them in errors.
				fmt.Printf("Setup key (enter manually if needed): %s\n", seed)
				fmt.Printf("otpauth URI (import into an authenticator app): %s\n", uri)
				err = confirmTOTPEnrollment(secret, os.Stdin, os.Stdout, time.Now)
				if err == nil {
					err = writeSecretFile(outPath, secret)
				}
				if err == nil {
					fmt.Printf("TOTP secret file created: %q\n", outPath)
					fmt.Println("Store the setup key securely and do not share this output.")
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "omni-totp: %v\n", err)
		os.Exit(1)
	}
}

func validateNewSecretPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("--out is required")
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("refusing to overwrite existing TOTP secret file")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect TOTP secret path: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect TOTP secret directory: %w", err)
	}
	if !parent.IsDir() {
		return errors.New("TOTP secret parent is not a directory")
	}
	return nil
}

func confirmTOTPEnrollment(secret []byte, input io.Reader, output io.Writer, now func() time.Time) error {
	verifier, err := authn.NewTOTPVerifier(secret, now)
	if err != nil {
		return fmt.Errorf("prepare TOTP confirmation: %w", err)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64), 64)
	for attempt := 1; attempt <= 3; attempt++ {
		fmt.Fprintf(output, "Enter the current 6-digit authenticator code (%d/3): ", attempt)
		if !scanner.Scan() {
			if scanErr := scanner.Err(); scanErr != nil {
				return errors.New("read TOTP confirmation code: input failed")
			}
			return errors.New("TOTP confirmation code is required")
		}
		if err := verifier.VerifyAndConsume(scanner.Text()); err == nil {
			fmt.Fprintln(output, "Authenticator code confirmed.")
			return nil
		}
		fmt.Fprintln(output, "Code did not match.")
	}
	return errors.New("TOTP enrollment was not confirmed; secret file was not created")
}

func generateTOTPSecret() ([]byte, error) {
	secret := make([]byte, generatedSecretSize)
	if n, err := rand.Read(secret); err != nil {
		return nil, errors.New("generate TOTP secret: cryptographic random source failed")
	} else if n != len(secret) {
		return nil, errors.New("generate TOTP secret: short random read")
	}
	return secret, nil
}

// writeSecretFile creates a confidential, unpadded-Base32 seed file. Lstat is
// an explicit symlink/existing-file check for a clear error; O_EXCL remains
// the race-safe protection against replacement between the check and create.
func writeSecretFile(path string, secret []byte) error {
	path = strings.TrimSpace(path)
	encoded, err := authn.EncodeTOTPSecret(secret)
	if err != nil {
		return fmt.Errorf("encode TOTP secret: %w", err)
	}
	if err := validateNewSecretPath(path); err != nil {
		return err
	}

	// #nosec G304 -- path is the administrator's explicit --out destination;
	// O_EXCL and the prior Lstat prevent overwrite and symlink replacement.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create TOTP secret file: %w", err)
	}
	createdInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("stat TOTP secret file: %w", statErr)
	}
	cleanup := func() {
		current, currentErr := os.Stat(path)
		if currentErr == nil && os.SameFile(current, createdInfo) {
			_ = os.Remove(path)
		}
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("set TOTP secret file permissions: %w", err)
	}
	if _, err := file.WriteString(encoded + "\n"); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("write TOTP secret file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("sync TOTP secret file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close TOTP secret file: %w", err)
	}
	return nil
}

func buildOTPAuthURI(issuer, account, secret string) (string, error) {
	if err := validateLabelPart("issuer", issuer); err != nil {
		return "", err
	}
	if err := validateLabelPart("account", account); err != nil {
		return "", err
	}
	if _, err := authn.DecodeTOTPSecret(secret); err != nil {
		return "", fmt.Errorf("invalid TOTP secret: %w", err)
	}

	// PathEscape protects the label from slash/control/path injection. Query
	// values are encoded independently by Values.Encode.
	label := url.PathEscape(issuer + ":" + account)
	query := url.Values{}
	query.Set("issuer", issuer)
	query.Set("secret", secret)
	return "otpauth://totp/" + label + "?" + query.Encode(), nil
}

func validateLabelPart(name, value string) error {
	if value == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s is too long", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}
