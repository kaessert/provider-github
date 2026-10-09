// Command sealedbox encrypts a value for the GitHub secrets API.
//
// GitHub accepts an organization secret only as a libsodium sealed box
// (crypto_box_seal) of the value under the organization's public key. This
// helper reads the plaintext on stdin and the base64 public key from its first
// argument, and prints the base64 sealed box. It exists so the end-to-end setup
// can create a disposable secret on a machine with neither libsodium nor a
// NaCl binding, and it uses only the standard library so it adds nothing to the
// provider's module graph.
//
// The construction is the one libsodium and golang.org/x/crypto/nacl/box
// (SealAnonymous) implement:
//
//	ephemeral keypair (epk, esk)
//	nonce  = BLAKE2b-192(epk || recipient)
//	key    = HSalsa20(X25519(esk, recipient), 0^16)
//	output = epk || XSalsa20-Poly1305(key, nonce, message)
//
// It is test tooling for a disposable value: the primitives below are not
// constant-time and must not be used to protect real secrets.
package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/bits"
	"os"
	"strings"
)

const keySize = 32

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "sealedbox: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: sealedbox <base64-public-key> < plaintext")
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(args[0]))
	if err != nil {
		return fmt.Errorf("public key is not base64: %w", err)
	}
	msg, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read plaintext: %w", err)
	}
	esk := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, esk); err != nil {
		return fmt.Errorf("generate ephemeral key: %w", err)
	}
	sealed, err := seal(esk, pub, msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, base64.StdEncoding.EncodeToString(sealed))
	return err
}

// seal returns the sealed box of msg for recipient, using esk as the ephemeral
// private key. esk must be 32 random bytes that are never reused.
func seal(esk, recipient, msg []byte) ([]byte, error) {
	if len(recipient) != keySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(recipient), keySize)
	}
	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(esk)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	pub, err := curve.NewPublicKey(recipient)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("key agreement: %w", err)
	}
	epk := priv.PublicKey().Bytes()

	var key [keySize]byte
	var zero [16]byte
	hsalsa20(&key, &zero, shared)

	nonceInput := make([]byte, 0, 2*keySize)
	nonceInput = append(nonceInput, epk...)
	nonceInput = append(nonceInput, recipient...)
	nonce := blake2b(nonceInput, 24)

	return append(epk, secretbox(msg, nonce, &key)...), nil
}

// secretbox is NaCl's crypto_secretbox: XSalsa20-Poly1305, tag first.
func secretbox(msg, nonce []byte, key *[keySize]byte) []byte {
	var sub [keySize]byte
	var n16 [16]byte
	copy(n16[:], nonce[:16])
	hsalsa20(&sub, &n16, key[:])

	// Block 0 of the stream keys Poly1305 (first 32 bytes) and masks the first
	// 32 message bytes; later blocks follow with an incrementing counter.
	stream := make([]byte, 32+len(msg))
	for block := 0; block*64 < len(stream); block++ {
		var out [64]byte
		salsa20Block(&out, nonce[16:24], uint64(block), &sub)
		copy(stream[block*64:], out[:])
	}
	ct := make([]byte, len(msg))
	for i := range msg {
		ct[i] = msg[i] ^ stream[32+i]
	}
	tag := poly1305(ct, stream[:32])
	return append(tag, ct...)
}

var sigma = [4]uint32{0x61707865, 0x3320646e, 0x79622d32, 0x6b206574}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// salsaRounds applies the 20 Salsa20 rounds to x in place.
func salsaRounds(x *[16]uint32) {
	qr := func(a, b, c, d int) {
		x[b] ^= bits.RotateLeft32(x[a]+x[d], 7)
		x[c] ^= bits.RotateLeft32(x[b]+x[a], 9)
		x[d] ^= bits.RotateLeft32(x[c]+x[b], 13)
		x[a] ^= bits.RotateLeft32(x[d]+x[c], 18)
	}
	for i := 0; i < 20; i += 2 {
		qr(0, 4, 8, 12)
		qr(5, 9, 13, 1)
		qr(10, 14, 2, 6)
		qr(15, 3, 7, 11)
		qr(0, 1, 2, 3)
		qr(5, 6, 7, 4)
		qr(10, 11, 8, 9)
		qr(15, 12, 13, 14)
	}
}

// hsalsa20 derives a 32-byte subkey from a 32-byte key and a 16-byte input.
func hsalsa20(out *[keySize]byte, in *[16]byte, key []byte) {
	var x [16]uint32
	x[0], x[5], x[10], x[15] = sigma[0], sigma[1], sigma[2], sigma[3]
	for i := 0; i < 4; i++ {
		x[1+i] = le32(key[4*i:])
		x[11+i] = le32(key[16+4*i:])
		x[6+i] = le32(in[4*i:])
	}
	salsaRounds(&x)
	for i, idx := range [8]int{0, 5, 10, 15, 6, 7, 8, 9} {
		putLE32(out[4*i:], x[idx])
	}
}

// salsa20Block produces one 64-byte block of the Salsa20 stream.
func salsa20Block(out *[64]byte, nonce []byte, counter uint64, key *[keySize]byte) {
	var in [16]uint32
	in[0], in[5], in[10], in[15] = sigma[0], sigma[1], sigma[2], sigma[3]
	for i := 0; i < 4; i++ {
		in[1+i] = le32(key[4*i:])
		in[11+i] = le32(key[16+4*i:])
	}
	in[6], in[7] = le32(nonce[0:]), le32(nonce[4:])
	in[8], in[9] = uint32(counter), uint32(counter>>32)

	x := in
	salsaRounds(&x)
	for i := range x {
		putLE32(out[4*i:], x[i]+in[i])
	}
}

// poly1305 returns the 16-byte one-time authenticator of msg under key.
func poly1305(msg, key []byte) []byte {
	leInt := func(b []byte) *big.Int {
		r := make([]byte, len(b))
		for i := range b {
			r[len(b)-1-i] = b[i]
		}
		return new(big.Int).SetBytes(r)
	}
	clamp := make([]byte, 16)
	copy(clamp, key[:16])
	for _, i := range []int{3, 7, 11, 15} {
		clamp[i] &= 15
	}
	for _, i := range []int{4, 8, 12} {
		clamp[i] &= 252
	}
	r := leInt(clamp)
	s := leInt(key[16:32])
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 130), big.NewInt(5))

	acc := new(big.Int)
	for off := 0; off < len(msg); off += 16 {
		end := min(off+16, len(msg))
		chunk := leInt(msg[off:end])
		chunk.SetBit(chunk, 8*(end-off), 1)
		acc.Add(acc, chunk)
		acc.Mul(acc, r)
		acc.Mod(acc, p)
	}
	acc.Add(acc, s)

	tag := make([]byte, 16)
	low := new(big.Int).And(acc, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)))
	b := low.Bytes()
	for i := range b {
		tag[i] = b[len(b)-1-i]
	}
	return tag
}

var blake2bIV = [8]uint64{
	0x6a09e667f3bcc908, 0xbb67ae8584caa73b, 0x3c6ef372fe94f82b, 0xa54ff53a5f1d36f1,
	0x510e527fade682d1, 0x9b05688c2b3e6c1f, 0x1f83d9abfb41bd6b, 0x5be0cd19137e2179,
}

var blake2bSigma = [10][16]byte{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
	{11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4},
	{7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
	{9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13},
	{2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
	{12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11},
	{13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
	{6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5},
	{10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
}

// blake2b returns the unkeyed BLAKE2b digest of data, size bytes long (1..64).
func blake2b(data []byte, size int) []byte {
	h := blake2bIV
	h[0] ^= 0x01010000 ^ uint64(size)

	compress := func(block []byte, t uint64, last bool) {
		var m [16]uint64
		for i := range m {
			for j := 7; j >= 0; j-- {
				m[i] = m[i]<<8 | uint64(block[8*i+j])
			}
		}
		var v [16]uint64
		copy(v[:8], h[:])
		copy(v[8:], blake2bIV[:])
		v[12] ^= t
		if last {
			v[14] = ^v[14]
		}
		g := func(a, b, c, d int, x, y uint64) {
			v[a] += v[b] + x
			v[d] = bits.RotateLeft64(v[d]^v[a], -32)
			v[c] += v[d]
			v[b] = bits.RotateLeft64(v[b]^v[c], -24)
			v[a] += v[b] + y
			v[d] = bits.RotateLeft64(v[d]^v[a], -16)
			v[c] += v[d]
			v[b] = bits.RotateLeft64(v[b]^v[c], -63)
		}
		for round := 0; round < 12; round++ {
			s := &blake2bSigma[round%10]
			g(0, 4, 8, 12, m[s[0]], m[s[1]])
			g(1, 5, 9, 13, m[s[2]], m[s[3]])
			g(2, 6, 10, 14, m[s[4]], m[s[5]])
			g(3, 7, 11, 15, m[s[6]], m[s[7]])
			g(0, 5, 10, 15, m[s[8]], m[s[9]])
			g(1, 6, 11, 12, m[s[10]], m[s[11]])
			g(2, 7, 8, 13, m[s[12]], m[s[13]])
			g(3, 4, 9, 14, m[s[14]], m[s[15]])
		}
		for i := range h {
			h[i] ^= v[i] ^ v[i+8]
		}
	}

	var t uint64
	for len(data) > 128 {
		t += 128
		compress(data[:128], t, false)
		data = data[128:]
	}
	var last [128]byte
	copy(last[:], data)
	t += uint64(len(data))
	compress(last[:], t, true)

	out := make([]byte, 64)
	for i, w := range h {
		for j := 0; j < 8; j++ {
			out[8*i+j] = byte(w >> (8 * j))
		}
	}
	return out[:size]
}
