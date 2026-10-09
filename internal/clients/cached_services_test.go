/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"strings"
	"testing"
)

// Credential layout is "appID,installationID,PEM"; both IDs are returned
// as plain strings (no integer parsing) so they can be used directly as
// Prometheus label values.
func TestExtractAppIDsHappyPath(t *testing.T) {
	creds := "12345,67890,-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----"

	appID, installID, err := ExtractAppIDs(creds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if appID != "12345" {
		t.Errorf("appID = %q, want %q", appID, "12345")
	}
	if installID != "67890" {
		t.Errorf("installID = %q, want %q", installID, "67890")
	}
}

// A malformed creds blob must error rather than silently return partial
// results that could leak into metric labels as garbage.
func TestExtractAppIDsTooFewFields(t *testing.T) {
	_, _, err := ExtractAppIDs("12345,only-two-fields")
	if err == nil {
		t.Fatal("expected error for 2-field input, got nil")
	}
	if !strings.Contains(err.Error(), "invalid format") {
		t.Errorf("error message = %q, want it to mention invalid format", err.Error())
	}
}

// Splitting on the first two commas is only safe because PEM bodies are
// base64 plus dashes/newlines — never commas. Encodes that invariant.
func TestExtractAppIDsPrivateKeyContainsNoCommas(t *testing.T) {
	creds := "1,2,-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA+/=\n-----END RSA PRIVATE KEY-----"

	appID, installID, err := ExtractAppIDs(creds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if appID != "1" || installID != "2" {
		t.Errorf("got (%q, %q), want (1, 2)", appID, installID)
	}
}
