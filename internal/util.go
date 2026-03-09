package internal

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
)

// HexToBytes decodes a hex string to bytes.
func HexToBytes(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// BytesToHex encodes bytes to a hex string.
func BytesToHex(b []byte) string {
	return hex.EncodeToString(b)
}

// ReverseBytes returns a reversed copy of the byte slice.
func ReverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i, v := range b {
		r[len(b)-1-i] = v
	}
	return r
}

// SwapEndian32 swaps endianness of each 4-byte word in the slice.
func SwapEndian32(b []byte) []byte {
	out := make([]byte, len(b))
	for i := 0; i+3 < len(b); i += 4 {
		out[i] = b[i+3]
		out[i+1] = b[i+2]
		out[i+2] = b[i+1]
		out[i+3] = b[i]
	}
	return out
}

// BytesToUint32LE reads a little-endian uint32 from bytes.
func BytesToUint32LE(b []byte) uint32 {
	return binary.LittleEndian.Uint32(b)
}

// Uint32LEToBytes writes a uint32 as little-endian bytes.
func Uint32LEToBytes(v uint32) [4]byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b
}

// DifficultyToTarget converts a pool difficulty to a 256-bit target.
// target = 0x00000000FFFF0000...0000 / difficulty
// Returns as [8]uint32 in little-endian word order (index 0 = least significant).
func DifficultyToTarget(diff float64) [8]uint32 {
	// Base target for difficulty 1 (Bitcoin)
	base := new(big.Int)
	base.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Scale: multiply by a large factor to maintain precision, then divide
	scale := new(big.Float).SetInt(base)
	divisor := new(big.Float).SetFloat64(diff)
	result := new(big.Float).Quo(scale, divisor)

	target, _ := result.Int(nil)

	var out [8]uint32
	targetBytes := target.Bytes()

	// Pad to 32 bytes
	padded := make([]byte, 32)
	copy(padded[32-len(targetBytes):], targetBytes)

	// Convert big-endian bytes to [8]uint32 little-endian word order
	// padded[0:4] is the most significant word -> out[7]
	for i := 0; i < 8; i++ {
		out[7-i] = binary.BigEndian.Uint32(padded[i*4 : i*4+4])
	}

	return out
}

// DoubleSHA256 computes SHA256(SHA256(data)).
func DoubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

// Bswap32 swaps endianness of a uint32.
func Bswap32(x uint32) uint32 {
	return ((x & 0xFF) << 24) | ((x & 0xFF00) << 8) |
		((x >> 8) & 0xFF00) | ((x >> 24) & 0xFF)
}

// FormatHashrate returns a human-readable hashrate string.
func FormatHashrate(hps float64) string {
	switch {
	case hps >= 1e12:
		return fmt.Sprintf("%.2f TH/s", hps/1e12)
	case hps >= 1e9:
		return fmt.Sprintf("%.2f GH/s", hps/1e9)
	case hps >= 1e6:
		return fmt.Sprintf("%.2f MH/s", hps/1e6)
	case hps >= 1e3:
		return fmt.Sprintf("%.2f kH/s", hps/1e3)
	default:
		return fmt.Sprintf("%.2f H/s", hps)
	}
}
