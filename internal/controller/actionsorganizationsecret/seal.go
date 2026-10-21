/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package actionsorganizationsecret

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"

	"github.com/google/go-github/v90/github"
	"golang.org/x/crypto/nacl/box"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

const (
	errGetPublicKey = "cannot get the organization public key"
	errBadPublicKey = "the organization public key is not a base64 encoded 32 byte key"
	errSeal         = "cannot seal the secret value"

	// staleKeyMessage is the text GitHub uses to reject a key_id that is no
	// longer the current one.
	staleKeyMessage = "is not the latest version"

	publicKeySize = 32
)

// put seals plaintext under the organization public key and sends it with req.
// The key is fetched for every write. GitHub rotates it, so a PUT can be
// rejected for naming a key that is no longer current; the key is then fetched
// once more and the write retried. Any other rejection is returned as it is.
func (c *external) put(ctx context.Context, org, name string, plaintext []byte, req github.SecretOrgRequest) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var pk *github.PublicKey
		pk, _, err = c.github.Actions.GetOrgPublicKey(ctx, org)
		if err != nil {
			return errors.Wrap(err, errGetPublicKey)
		}
		req.KeyID = pk.GetKeyID()
		req.EncryptedValue, err = seal(pk.GetKey(), plaintext)
		if err != nil {
			return err
		}
		_, err = c.github.Actions.CreateOrUpdateOrgSecret(ctx, org, name, req)
		if err == nil || !isStaleKey(err) {
			break
		}
	}
	return errors.Wrap(err, errPutSecret)
}

// seal returns plaintext sealed to the base64 encoded public key, base64
// encoded: a libsodium sealed box, which is what GitHub decrypts.
func seal(publicKey string, plaintext []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(raw) != publicKeySize {
		return "", errors.New(errBadPublicKey)
	}
	var pk [publicKeySize]byte
	copy(pk[:], raw)
	sealed, err := box.SealAnonymous(nil, plaintext, &pk, rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, errSeal)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// isStaleKey reports whether err is GitHub's 422 for a key_id that is not the
// latest version of the organization public key.
func isStaleKey(err error) bool {
	var er *github.ErrorResponse
	if !errors.As(err, &er) || er.Response == nil || er.Response.StatusCode != 422 {
		return false
	}
	if strings.Contains(er.Message, staleKeyMessage) {
		return true
	}
	for _, e := range er.Errors {
		if strings.Contains(e.Message, staleKeyMessage) {
			return true
		}
	}
	return false
}
