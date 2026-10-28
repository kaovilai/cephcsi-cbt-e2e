package data

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
)

// TestZeroBlockHash verifies that zeroBlockHash returns the correct SHA-256 of a
// zero-filled byte slice for various sizes. The hash for the empty case is checked
// against the canonical SHA-256 of an empty input so that any accidental algorithm
// swap is caught without needing an external oracle for every size.
func TestZeroBlockHash(t *testing.T) {
	// SHA-256 of an empty input is a well-known constant.
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	// 3*32KiB+7 crosses the streaming buffer boundary.
	sizes := []int64{0, 512, 4096, 3*32*1024 + 7, DefaultBlockSize}
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
	for _, size := range []int64{0, 4096, 3*32*1024 + 7, DefaultBlockSize} {
		first := zeroBlockHash(size)
		for i := range 5 {
			if got := zeroBlockHash(size); got != first {
				t.Errorf("zeroBlockHash(%d) is not deterministic: call 0 = %q, call %d = %q", size, first, i+1, got)
			}
		}
	}
}

// TestCachedZeroBlockHash verifies the cache returns the same value as the
// uncached computation and stays correct under concurrent use.
func TestCachedZeroBlockHash(t *testing.T) {
	sizes := []int64{0, 4096, 3*32*1024 + 7, DefaultBlockSize}
	for _, size := range sizes {
		if got, want := cachedZeroBlockHash(size), zeroBlockHash(size); got != want {
			t.Fatalf("cachedZeroBlockHash(%d) = %q, want %q", size, got, want)
		}
	}

	want := make(map[int64]string, len(sizes))
	for _, size := range sizes {
		want[size] = zeroBlockHash(size)
	}

	const perSize = 4
	type result struct {
		size int64
		hash string
	}
	results := make(chan result, len(sizes)*perSize)
	for _, size := range sizes {
		for range perSize {
			go func(s int64) {
				results <- result{size: s, hash: cachedZeroBlockHash(s)}
			}(size)
		}
	}
	for range len(sizes) * perSize {
		got := <-results
		if got.hash != want[got.size] {
			t.Errorf("concurrent cachedZeroBlockHash(%d) = %q, want %q", got.size, got.hash, want[got.size])
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
			want:     filepath.Join(DefaultMountPath, "test.txt"),
		},
		{
			name:     "empty filename",
			filename: "",
			want:     DefaultMountPath,
		},
		{
			name:     "nested path",
			filename: "subdir/file.txt",
			want:     filepath.Join(DefaultMountPath, "subdir/file.txt"),
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

func TestSafeMountPath(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
		wantErr  bool
	}{
		{name: "simple", filename: "test.txt", want: filepath.Join(DefaultMountPath, "test.txt")},
		{name: "empty", filename: "", want: DefaultMountPath},
		{name: "nested", filename: "subdir/file.txt", want: filepath.Join(DefaultMountPath, "subdir/file.txt")},
		{name: "inner traversal stays inside", filename: "subdir/../ok.txt", want: filepath.Join(DefaultMountPath, "ok.txt")},
		{name: "parent escape", filename: "../etc/passwd", wantErr: true},
		{name: "nested escape", filename: "a/../../etc/passwd", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeMountPath(tc.filename)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("safeMountPath(%q) = %q, want error", tc.filename, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeMountPath(%q) error = %v", tc.filename, err)
			}
			if got != tc.want {
				t.Errorf("safeMountPath(%q) = %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}

func TestBlockWriteCommand(t *testing.T) {
	cmd := blockWriteCommand(4096, 1024, 0xAA)
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("blockWriteCommand() = %v, want a sh -c command", cmd)
	}
	for _, want := range []string{"count=1024", "seek=4096", `\xaa`, DefaultDevicePath, "conv=notrunc,fsync"} {
		if !strings.Contains(cmd[2], want) {
			t.Errorf("blockWriteCommand() script %q does not contain %q", cmd[2], want)
		}
	}
}

func TestReadBlockHashCommand(t *testing.T) {
	cmd := readBlockHashCommand(8192, 512)
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("readBlockHashCommand() = %v, want a sh -c command", cmd)
	}
	for _, want := range []string{"skip=8192", "count=512", DefaultDevicePath, "sha256sum"} {
		if !strings.Contains(cmd[2], want) {
			t.Errorf("readBlockHashCommand() script %q does not contain %q", cmd[2], want)
		}
	}
}

func TestWriteFileCommand(t *testing.T) {
	filePath := filepath.Join(DefaultMountPath, "a'b.txt")
	content := "line1\nline2 with 'quotes' and $ymbols"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	cmd := writeFileCommand(filePath, content)
	if len(cmd) != 5 {
		t.Fatalf("writeFileCommand() = %v, want sh -c script _ path", cmd)
	}
	if !strings.Contains(cmd[2], encoded) {
		t.Errorf("script %q does not carry the base64 content %q", cmd[2], encoded)
	}
	if strings.Contains(cmd[2], filePath) {
		t.Errorf("script %q must not interpolate the path; pass it as an argument", cmd[2])
	}
	if cmd[3] != "_" || cmd[4] != filePath {
		t.Errorf("writeFileCommand() args = %q, want [\"_\" %q]", cmd[3:], filePath)
	}
}

func TestReadFileHashCommand(t *testing.T) {
	filePath := filepath.Join(DefaultMountPath, "a'b.txt")
	cmd := readFileHashCommand(filePath)
	if len(cmd) != 5 {
		t.Fatalf("readFileHashCommand() = %v, want sh -c script _ path", cmd)
	}
	if !strings.Contains(cmd[2], `sha256sum "$1"`) {
		t.Errorf("script %q should hash the path argument", cmd[2])
	}
	if cmd[3] != "_" || cmd[4] != filePath {
		t.Errorf("readFileHashCommand() args = %q, want [\"_\" %q]", cmd[3:], filePath)
	}
}

func TestVerifyAllocatedBlocks(t *testing.T) {
	zero := zeroBlockHash(DefaultBlockSize)
	nonZero := strings.Repeat("a", 64)
	readErr := errors.New("read boom")

	tests := []struct {
		name    string
		hash    string
		err     error
		wantErr string
	}{
		{name: "non-zero content", hash: nonZero},
		{name: "zero content", hash: zero, wantErr: "contains only zeros"},
		{name: "read error", err: readErr, wantErr: "read block at offset 0"},
	}
	result := &cbt.MetadataResult{Blocks: []cbt.BlockMetadata{{ByteOffset: 0, SizeBytes: DefaultBlockSize}}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			read := func(string, int64, int64) (string, error) { return tc.hash, tc.err }
			err := verifyAllocatedBlocks(read, "pod", result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyAllocatedBlocks() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("verifyAllocatedBlocks() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyChangedBlocks(t *testing.T) {
	result := &cbt.MetadataResult{Blocks: []cbt.BlockMetadata{{ByteOffset: 4096, SizeBytes: 4096}}}
	readErr := errors.New("read boom")

	tests := []struct {
		name    string
		base    string
		target  string
		baseErr error
		tgtErr  error
		wantErr string
	}{
		{name: "content differs", base: "aaa", target: "bbb"},
		{name: "content identical", base: "aaa", target: "aaa", wantErr: "content is identical"},
		{name: "base read error", baseErr: readErr, wantErr: "read base block at offset 4096"},
		{name: "target read error", base: "aaa", tgtErr: readErr, wantErr: "read target block at offset 4096"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			read := func(pod string, _, _ int64) (string, error) {
				if pod == "base" {
					return tc.base, tc.baseErr
				}
				return tc.target, tc.tgtErr
			}
			err := verifyChangedBlocks(read, "base", "target", result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyChangedBlocks() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("verifyChangedBlocks() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyUnchangedBlocksNotReported(t *testing.T) {
	const volumeSize = 3 * DefaultBlockSize
	// The middle block is reported as changed and must not be read.
	result := &cbt.MetadataResult{Blocks: []cbt.BlockMetadata{{ByteOffset: DefaultBlockSize, SizeBytes: DefaultBlockSize}}}

	tests := []struct {
		name    string
		base    map[int64]string
		target  map[int64]string
		err     error
		wantErr string
	}{
		{
			name:   "unchanged blocks match",
			base:   map[int64]string{0: "a", 2 * DefaultBlockSize: "c"},
			target: map[int64]string{0: "a", 2 * DefaultBlockSize: "c"},
		},
		{
			name:    "unreported block differs",
			base:    map[int64]string{0: "a", 2 * DefaultBlockSize: "c"},
			target:  map[int64]string{0: "changed", 2 * DefaultBlockSize: "c"},
			wantErr: "NOT reported as changed but content differs",
		},
		{
			name:    "read error",
			err:     errors.New("read boom"),
			wantErr: "base pod",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			read := func(pod string, offset, _ int64) (string, error) {
				if tc.err != nil {
					return "", tc.err
				}
				if offset == DefaultBlockSize {
					t.Fatalf("read of reported block at offset %d", offset)
				}
				if pod == "base" {
					return tc.base[offset], nil
				}
				return tc.target[offset], nil
			}
			err := verifyUnchangedBlocksNotReported(read, "base", "target", result, volumeSize)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyUnchangedBlocksNotReported() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("verifyUnchangedBlocksNotReported() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
