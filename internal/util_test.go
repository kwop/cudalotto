package internal

import (
	"encoding/hex"
	"testing"
)

func TestHexToBytes(t *testing.T) {
	tests := []struct {
		input string
		want  []byte
		err   bool
	}{
		{"", []byte{}, false},
		{"deadbeef", []byte{0xde, 0xad, 0xbe, 0xef}, false},
		{"00ff", []byte{0x00, 0xff}, false},
		{"zz", nil, true},
		{"abc", nil, true}, // odd length
	}
	for _, tt := range tests {
		got, err := HexToBytes(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("HexToBytes(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if err == nil && string(got) != string(tt.want) {
			t.Errorf("HexToBytes(%q) = %x, want %x", tt.input, got, tt.want)
		}
	}
}

func TestBytesToHex(t *testing.T) {
	tests := []struct {
		input []byte
		want  string
	}{
		{[]byte{0xde, 0xad, 0xbe, 0xef}, "deadbeef"},
		{[]byte{}, ""},
		{[]byte{0x00}, "00"},
	}
	for _, tt := range tests {
		got := BytesToHex(tt.input)
		if got != tt.want {
			t.Errorf("BytesToHex(%x) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReverseBytes(t *testing.T) {
	tests := []struct {
		input []byte
		want  []byte
	}{
		{[]byte{1, 2, 3, 4}, []byte{4, 3, 2, 1}},
		{[]byte{0xff}, []byte{0xff}},
		{[]byte{}, []byte{}},
	}
	for _, tt := range tests {
		got := ReverseBytes(tt.input)
		if string(got) != string(tt.want) {
			t.Errorf("ReverseBytes(%x) = %x, want %x", tt.input, got, tt.want)
		}
	}
}

func TestSwapEndian32(t *testing.T) {
	tests := []struct {
		input []byte
		want  []byte
	}{
		{[]byte{0x01, 0x02, 0x03, 0x04}, []byte{0x04, 0x03, 0x02, 0x01}},
		{
			[]byte{0x01, 0x02, 0x03, 0x04, 0xAA, 0xBB, 0xCC, 0xDD},
			[]byte{0x04, 0x03, 0x02, 0x01, 0xDD, 0xCC, 0xBB, 0xAA},
		},
		{[]byte{}, []byte{}},
	}
	for _, tt := range tests {
		got := SwapEndian32(tt.input)
		if string(got) != string(tt.want) {
			t.Errorf("SwapEndian32(%x) = %x, want %x", tt.input, got, tt.want)
		}
	}
}

func TestSwapEndian32_RoundTrip(t *testing.T) {
	input := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	got := SwapEndian32(SwapEndian32(input))
	if string(got) != string(input) {
		t.Errorf("SwapEndian32 round trip failed: %x != %x", got, input)
	}
}

func TestBytesToUint32LE(t *testing.T) {
	// 0x04030201 in little-endian = bytes [0x01, 0x02, 0x03, 0x04]
	got := BytesToUint32LE([]byte{0x01, 0x02, 0x03, 0x04})
	want := uint32(0x04030201)
	if got != want {
		t.Errorf("BytesToUint32LE = 0x%08x, want 0x%08x", got, want)
	}
}

func TestUint32LEToBytes(t *testing.T) {
	got := Uint32LEToBytes(0x04030201)
	want := [4]byte{0x01, 0x02, 0x03, 0x04}
	if got != want {
		t.Errorf("Uint32LEToBytes = %x, want %x", got, want)
	}
}

func TestUint32LE_RoundTrip(t *testing.T) {
	val := uint32(0xDEADBEEF)
	b := Uint32LEToBytes(val)
	got := BytesToUint32LE(b[:])
	if got != val {
		t.Errorf("round trip: got 0x%08x, want 0x%08x", got, val)
	}
}

func TestDifficultyToTarget(t *testing.T) {
	// Difficulty 1: target = 0x00000000FFFF0000...0000
	target := DifficultyToTarget(1.0)
	// out[7] should be 0x00000000, out[6] should be 0xFFFF0000
	if target[7] != 0x00000000 {
		t.Errorf("diff=1 target[7] = 0x%08x, want 0x00000000", target[7])
	}
	if target[6] != 0xFFFF0000 {
		t.Errorf("diff=1 target[6] = 0x%08x, want 0xFFFF0000", target[6])
	}
	// Lower words should be 0
	for i := 0; i < 6; i++ {
		if target[i] != 0 {
			t.Errorf("diff=1 target[%d] = 0x%08x, want 0x00000000", i, target[i])
		}
	}

	// Difficulty 2: target halved
	target2 := DifficultyToTarget(2.0)
	if target2[6] != 0x7FFF8000 {
		t.Errorf("diff=2 target[6] = 0x%08x, want 0x7FFF8000", target2[6])
	}

	// Higher difficulty = smaller target
	target10k := DifficultyToTarget(10000.0)
	if target10k[7] != 0 {
		t.Errorf("diff=10000 target[7] should be 0")
	}
	// target10k should be much smaller than target1
	if target10k[6] >= target[6] {
		t.Errorf("diff=10000 target should be smaller than diff=1 target")
	}
}

func TestDoubleSHA256_Empty(t *testing.T) {
	got := DoubleSHA256([]byte{})
	wantHex := "5df6e0e2761359d30a8275058e299fcc0381534545f55cf43e41983f5d4c9456"
	gotHex := hex.EncodeToString(got[:])
	if gotHex != wantHex {
		t.Errorf("DoubleSHA256(\"\") = %s, want %s", gotHex, wantHex)
	}
}

func TestDoubleSHA256_Hello(t *testing.T) {
	got := DoubleSHA256([]byte("hello"))
	wantHex := "9595c9df90075148eb06860365df33584b75bff782a510c6cd4883a419833d50"
	gotHex := hex.EncodeToString(got[:])
	if gotHex != wantHex {
		t.Errorf("DoubleSHA256(\"hello\") = %s, want %s", gotHex, wantHex)
	}
}

func TestDoubleSHA256_BitcoinGenesis(t *testing.T) {
	headerHex := "0100000000000000000000000000000000000000000000000000000000000000" +
		"000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa" +
		"4b1e5e4a29ab5f49ffff001d1dac2b7c"
	header, _ := hex.DecodeString(headerHex)
	got := DoubleSHA256(header)
	wantHex := "6fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000"
	gotHex := hex.EncodeToString(got[:])
	if gotHex != wantHex {
		t.Errorf("DoubleSHA256(genesis) = %s, want %s", gotHex, wantHex)
	}
}

func TestBswap32(t *testing.T) {
	tests := []struct {
		input uint32
		want  uint32
	}{
		{0x01020304, 0x04030201},
		{0xDEADBEEF, 0xEFBEADDE},
		{0x00000000, 0x00000000},
		{0xFFFFFFFF, 0xFFFFFFFF},
		{0xFF000000, 0x000000FF},
	}
	for _, tt := range tests {
		got := Bswap32(tt.input)
		if got != tt.want {
			t.Errorf("Bswap32(0x%08x) = 0x%08x, want 0x%08x", tt.input, got, tt.want)
		}
	}
}

func TestBswap32_RoundTrip(t *testing.T) {
	val := uint32(0xCAFEBABE)
	if Bswap32(Bswap32(val)) != val {
		t.Error("Bswap32 round trip failed")
	}
}

func TestFormatHashrate(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{500, "500.00 H/s"},
		{1500, "1.50 kH/s"},
		{2_500_000, "2.50 MH/s"},
		{3_500_000_000, "3.50 GH/s"},
		{4_500_000_000_000, "4.50 TH/s"},
		{0, "0.00 H/s"},
	}
	for _, tt := range tests {
		got := FormatHashrate(tt.input)
		if got != tt.want {
			t.Errorf("FormatHashrate(%f) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
