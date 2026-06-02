package frame

import (
	"testing"
)

func TestEncodeVarint(t *testing.T) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7F}},
		{128, []byte{0x80, 0x01}},
		{255, []byte{0xFF, 0x01}},
		{0x4000, []byte{0x80, 0x80, 0x01}},
		{0xFFFFFFFF, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x0F}},
	}

	for _, tc := range tests {
		result := appendVarint(nil, tc.value)
		if !bytesEqual(result, tc.expected) {
			t.Errorf("varint(%d): got %x, want %x", tc.value, result, tc.expected)
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
