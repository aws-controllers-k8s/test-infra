// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package dod

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// A dod job's verdict.json lives in the Prow logs bucket, which presubmits
// running PR code can write. The Dispatcher trusts a dod verdict only with
// an Ed25519 signature over its exact bytes, made with a key the sandboxed
// build uid cannot read (Plan 9 review). sdk-bump verdicts need none: they
// are bound to the PR body's Gate-Verdict trailer.

var (
	ErrSignatureMissing = errors.New("the verdict has no signature")
	ErrSignatureInvalid = errors.New("the verdict's signature is not valid")
)

// maxKeyFile bounds the key file read: a base64 seed is 44 bytes.
const maxKeyFile = 4 << 10

// LoadSigningKey reads a base64 (std) 32-byte Ed25519 seed. It refuses a
// symlink, a file whose mode has any group or other bit, and any content
// that does not decode to exactly 32 bytes.
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("signing key %s is a symlink", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("signing key %s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("signing key %s has mode %#o; group and other must have no access", path, perm)
	}
	if fi.Size() > maxKeyFile {
		return nil, fmt.Errorf("signing key %s is too large", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(b)))
	if err != nil {
		return nil, fmt.Errorf("signing key %s is not base64: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key %s decodes to %d bytes, want 32 bytes", path, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ParsePublicKey decodes a base64 (std) 32-byte Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("dod public key is not base64: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("dod public key decodes to %d bytes, want 32", len(b))
	}
	return ed25519.PublicKey(b), nil
}

// Sign returns the verdict.sig content: base64 (std) of the Ed25519
// signature over verdictBytes, then a newline.
func Sign(priv ed25519.PrivateKey, verdictBytes []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, verdictBytes)) + "\n")
}

// VerifySignature checks a verdict.sig file against the exact verdict bytes.
func VerifySignature(pub ed25519.PublicKey, verdictBytes, sigFile []byte) error {
	s := bytes.TrimSpace(sigFile)
	if len(s) == 0 {
		return ErrSignatureMissing
	}
	sig, err := base64.StdEncoding.DecodeString(string(s))
	if err != nil || len(sig) != ed25519.SignatureSize || len(pub) != ed25519.PublicKeySize {
		return ErrSignatureInvalid
	}
	if !ed25519.Verify(pub, verdictBytes, sig) {
		return ErrSignatureInvalid
	}
	return nil
}

// GenerateKey makes a new signing key: the base64 seed (the secret) and the
// base64 public key.
func GenerateKey() (seedB64, pubB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(pub), nil
}
