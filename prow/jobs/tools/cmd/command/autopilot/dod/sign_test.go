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
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeKey(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(p, []byte(content), mode))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func TestSignRoundTrip(t *testing.T) {
	seed, pubB64, err := GenerateKey()
	require.NoError(t, err)
	priv, err := LoadSigningKey(writeKey(t, "  "+seed+"\n", 0o600))
	require.NoError(t, err)
	pub, err := ParsePublicKey(pubB64)
	require.NoError(t, err)
	assert.Equal(t, priv.Public(), pub)

	verdict := []byte(`{"schema":"ack.autopilot.verdict/v1"}` + "\n")
	sig := Sign(priv, verdict)
	assert.Equal(t, byte('\n'), sig[len(sig)-1])
	require.NoError(t, VerifySignature(pub, verdict, sig))

	tampered := append([]byte(nil), verdict...)
	tampered[3] ^= 1
	assert.True(t, errors.Is(VerifySignature(pub, tampered, sig), ErrSignatureInvalid))

	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 1
	assert.True(t, errors.Is(VerifySignature(pub, verdict, badSig), ErrSignatureInvalid))
	assert.True(t, errors.Is(VerifySignature(pub, verdict, []byte("not base64!")), ErrSignatureInvalid))
	assert.True(t, errors.Is(VerifySignature(pub, verdict, nil), ErrSignatureMissing))
	assert.True(t, errors.Is(VerifySignature(pub, verdict, []byte(" \n")), ErrSignatureMissing))

	_, otherPub, err := GenerateKey()
	require.NoError(t, err)
	op, err := ParsePublicKey(otherPub)
	require.NoError(t, err)
	assert.True(t, errors.Is(VerifySignature(op, verdict, sig), ErrSignatureInvalid))
	assert.True(t, errors.Is(VerifySignature(nil, verdict, sig), ErrSignatureInvalid))
}

func TestLoadSigningKeyRefuses(t *testing.T) {
	seed, _, err := GenerateKey()
	require.NoError(t, err)

	_, err = LoadSigningKey(writeKey(t, seed, 0o644))
	assert.ErrorContains(t, err, "mode")
	_, err = LoadSigningKey(writeKey(t, seed, 0o640))
	assert.ErrorContains(t, err, "mode")

	target := writeKey(t, seed, 0o600)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))
	_, err = LoadSigningKey(link)
	assert.ErrorContains(t, err, "symlink")

	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	_, err = LoadSigningKey(writeKey(t, short, 0o600))
	assert.ErrorContains(t, err, "32 bytes")
	long := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize))
	_, err = LoadSigningKey(writeKey(t, long, 0o600))
	assert.ErrorContains(t, err, "32 bytes")
	_, err = LoadSigningKey(writeKey(t, "%%%", 0o600))
	assert.Error(t, err)
	_, err = LoadSigningKey(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
	_, err = LoadSigningKey(t.TempDir())
	assert.Error(t, err, "a directory is not a key file")
}

func TestParsePublicKey(t *testing.T) {
	_, err := ParsePublicKey(base64.StdEncoding.EncodeToString(make([]byte, 31)))
	assert.Error(t, err)
	_, err = ParsePublicKey("***")
	assert.Error(t, err)
	_, err = ParsePublicKey("")
	assert.Error(t, err)
	k, err := ParsePublicKey(" " + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n")
	require.NoError(t, err)
	assert.Len(t, k, ed25519.PublicKeySize)
}
