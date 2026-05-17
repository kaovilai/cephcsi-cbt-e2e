package data

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// TestZeroBlockHash verifies that zeroBlockHash returns the correct SHA-256 of a
// zero-filled byte slice for various sizes. The hash for the empty case is checked
// against the canonical SHA-256 of an empty input so that any accidental algorithm
// swap is caught without needing an external oracle for every size.
func TestZeroBlockHash(t *testing.T) {
	// SHA-256 of an empty input is a well-known constant.
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	sizes := []int64{0, 512, 4096, DefaultBlockSize}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			got := zeroBlockHash(size)

			// Verify against independently computed value using stdlib.
			h := sha256.New()
			h.Write(make([]byte, size))
			want := hex.EncodeToString(h.Sum(nil))

			if got != want {
				t.Errorf("zeroBlockHash(%d) = %q, want %q", size, got, want)
			}

			// For size 0, also cross-check against the well-known constant.
			if size == 0 && got != emptyHash {
				t.Errorf("zeroBlockHash(0) = %q, want canonical empty SHA-256 %q", got, emptyHash)
			}

			// Result must be a 64-character lowercase hex string (SHA-256 produces 32 bytes).
			if len(got) != 64 {
				t.Errorf("zeroBlockHash(%d) returned %d chars, want 64", size, len(got))
			}
		})
	}
}

// TestZeroBlockHashDifferentSizes verifies that distinct sizes produce distinct hashes,
// since a zero block of one size is not equal to one of a different size.
func TestZeroBlockHashDifferentSizes(t *testing.T) {
	sizes := []int64{0, 512, 4096, DefaultBlockSize}
	seen := make(map[string]int64)
	for _, size := range sizes {
		h := zeroBlockHash(size)
		if prev, exists := seen[h]; exists {
			t.Errorf("zeroBlockHash(%d) and zeroBlockHash(%d) returned the same hash %q", prev, size, h)
		}
		seen[h] = size
	}
}

// TestZeroBlockHashDeterministic verifies that repeated calls with the same size
// always return the same value.
func TestZeroBlockHashDeterministic(t *testing.T) {
	for _, size := range []int64{0, 4096, DefaultBlockSize} {
		first := zeroBlockHash(size)
		for i := 0; i < 5; i++ {
			if got := zeroBlockHash(size); got != first {
				t.Errorf("zeroBlockHash(%d) is not deterministic: call 0 = %q, call %d = %q", size, first, i+1, got)
			}
		}
	}
}

// TestMountFilePath verifies the path construction for Filesystem-mode PVC files.
func TestMountFilePath(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			name:     "simple filename",
			filename: "test.txt",
			want:     DefaultMountPath + "/test.txt",
		},
		{
			name:     "empty filename",
			filename: "",
			want:     DefaultMountPath,
		},
		{
			name:     "nested path",
			filename: "subdir/file.txt",
			want:     DefaultMountPath + "/subdir/file.txt",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mountFilePath(tc.filename)
			if got != tc.want {
				t.Errorf("mountFilePath(%q) = %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}
