package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// det returns n deterministic bytes derived from label.
func det(label string, n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; len(out) < n; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", label, i)))
		out = append(out, h[:]...)
	}
	return out[:n]
}

const (
	// recipientPub is the X25519 public key of the private key det("recipient", 32).
	recipientPub = "dde8dae64758ca4e291e2de7c380c8deb89ac091c1735425652e446a47c8ee48"
	// sealed1 is the box of the single message byte det("msg1", 1) under ephemeral
	// key det("ephemeral", 32), as produced by golang.org/x/crypto/nacl/box
	// SealAnonymous with the same inputs.
	sealed1 = "fff2d5c7b9d89306395ebed86280b3a9279f24931ceb52cd96ddbc62f8bdbc6354657d24b959fc30638466e3684f6eb018"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSeal compares the box against golang.org/x/crypto/nacl/box output for
// message lengths around every block boundary of the cipher (32-byte Poly1305
// key offset, 64-byte Salsa20 blocks) and of the 16-byte Poly1305 chunks.
func TestSeal(t *testing.T) {
	// SHA-256 of the full box, per message length.
	cases := map[string]struct {
		n    int
		want string
	}{
		"Empty":          {0, "fc9ad9c8e89799d85dece590b0860741999cd99bb83527b6a64d339814857c40"},
		"OneByte":        {1, "2e8b6aeb9328f92dc9b2c2810c51107b930594401f0bb572e190639b1d1ae4a1"},
		"Thirty1Bytes":   {31, "6f62c1d3672228a0c6966c66e2f8f80f3a0c92fbf665c3090d4fca87a7107a3d"},
		"Thirty2Bytes":   {32, "2c1494d95e29054b4638bc3d2eab29006253b2f16fdc1a9bdc809a290b798bb0"},
		"Thirty3Bytes":   {33, "b71223a485e9f674d7f8c09fbdafa34f538df34097307283fcea1b7ef43a035c"},
		"Sixty3Bytes":    {63, "541eeffe75bd5a99dc64d9c697b148d89bd4a76fcd10f8da57f11958c38544ee"},
		"Sixty4Bytes":    {64, "9d9c148bfb2688febbe161fa4780aab2399ebe40f92357d31eeac1146635a890"},
		"Sixty5Bytes":    {65, "673be15c49bb95eb3009d87bf2fe74e3df5417aad9e985bfa5f1d313b6b973a8"},
		"TwoHundredByte": {200, "914ab26524c808ac729518ffb0a4b5775e9384e46e25df778ebb2d0a63d2b509"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := seal(det("ephemeral", 32), mustHex(t, recipientPub), det(fmt.Sprintf("msg%d", tc.n), tc.n))
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != tc.want {
				t.Errorf("sha256(box) = %x, want %s", sum, tc.want)
			}
			if wantLen := 32 + 16 + tc.n; len(got) != wantLen {
				t.Errorf("len(box) = %d, want %d", len(got), wantLen)
			}
		})
	}

	t.Run("FullBox", func(t *testing.T) {
		got, err := seal(det("ephemeral", 32), mustHex(t, recipientPub), det("msg1", 1))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if hex.EncodeToString(got) != sealed1 {
			t.Errorf("box = %x, want %s", got, sealed1)
		}
	})
}

func TestSealErrors(t *testing.T) {
	cases := map[string]struct {
		esk, pub []byte
		reason   string
	}{
		"ShortPublicKey":    {det("e", 32), det("p", 31), "public key is 31 bytes"},
		"ShortEphemeralKey": {det("e", 31), mustHex(t, recipientPub), "ephemeral key"},
		"LowOrderPublicKey": {det("e", 32), make([]byte, 32), "key agreement"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := seal(tc.esk, tc.pub, []byte("x"))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %v, want it to contain %q", err, tc.reason)
			}
		})
	}
}

// TestBlake2b checks the published BLAKE2b-512 test vector for "abc".
func TestBlake2b(t *testing.T) {
	want := "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d1" +
		"7d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923"
	if got := hex.EncodeToString(blake2b([]byte("abc"), 64)); got != want {
		t.Errorf("blake2b-512(abc) = %s, want %s", got, want)
	}
	if got := len(blake2b(nil, 24)); got != 24 {
		t.Errorf("blake2b-192 length = %d, want 24", got)
	}
	// Inputs longer than one 128-byte block exercise the multi-block path.
	long := blake2b(bytes.Repeat([]byte{'a'}, 300), 64)
	if bytes.Equal(long, blake2b(bytes.Repeat([]byte{'a'}, 299), 64)) {
		t.Error("blake2b ignores the last input byte")
	}
}

func TestRun(t *testing.T) {
	cases := map[string]struct {
		args   []string
		stdin  string
		reason string
	}{
		"Success":        {[]string{base64.StdEncoding.EncodeToString(mustHex(t, recipientPub))}, "value", ""},
		"NoArguments":    {nil, "value", "usage"},
		"NotBase64":      {[]string{"%%%"}, "value", "not base64"},
		"WrongKeyLength": {[]string{base64.StdEncoding.EncodeToString([]byte("short"))}, "value", "want 32"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(tc.args, strings.NewReader(tc.stdin), &out)
			if tc.reason != "" {
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
			if err != nil {
				t.Fatalf("output is not base64: %v", err)
			}
			if want := 32 + 16 + len(tc.stdin); len(raw) != want {
				t.Errorf("decoded length = %d, want %d", len(raw), want)
			}
		})
	}
}
