package cbt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/api"
	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/iterator"
	"k8s.io/client-go/rest"
)

func makeResult(blocks ...BlockMetadata) *MetadataResult {
	return &MetadataResult{Blocks: blocks}
}

func block(offset, size int64) BlockMetadata {
	return BlockMetadata{ByteOffset: offset, SizeBytes: size}
}

func TestNewClient(t *testing.T) {
	c, err := NewClient(&rest.Config{Host: "https://example.invalid"}, "test-ns", "sa-ns", "sa")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.namespace != "test-ns" || c.saNamespace != "sa-ns" || c.saName != "sa" {
		t.Errorf("NewClient() stored namespace=%q saNamespace=%q saName=%q, want test-ns/sa-ns/sa",
			c.namespace, c.saNamespace, c.saName)
	}
	if c.clients.KubeClient == nil || c.clients.SnapshotClient == nil || c.clients.SmsCRClient == nil {
		t.Error("NewClient() did not build all clients")
	}
}

func TestNewResultAndArgs(t *testing.T) {
	c, err := NewClient(&rest.Config{Host: "https://example.invalid"}, "test-ns", "sa-ns", "sa")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, args := c.newResultAndArgs()
	if result == nil {
		t.Fatal("newResultAndArgs() returned nil result")
	}
	if args.Emitter == nil {
		t.Fatal("newResultAndArgs() returned nil Emitter")
	}
	if args.Namespace != "test-ns" || args.SANamespace != "sa-ns" || args.SAName != "sa" {
		t.Errorf("args namespace=%q saNamespace=%q saName=%q, want test-ns/sa-ns/sa",
			args.Namespace, args.SANamespace, args.SAName)
	}

	// The base args must pass upstream validation once the per-call fields are set.
	args.SnapshotName = "target-snapshot"
	if err := args.Validate(); err != nil {
		t.Fatalf("iterator.Args.Validate() error = %v", err)
	}

	// Records must accumulate into the same result, and the stream metadata must
	// reflect the latest record.
	first := iterator.IteratorMetadata{
		BlockMetadataType:   api.BlockMetadataType_VARIABLE_LENGTH,
		VolumeCapacityBytes: 1 << 30,
		BlockMetadata:       []*api.BlockMetadata{{ByteOffset: 0, SizeBytes: 4096}},
	}
	second := iterator.IteratorMetadata{
		BlockMetadataType:   api.BlockMetadataType_VARIABLE_LENGTH,
		VolumeCapacityBytes: 2 << 30,
		BlockMetadata:       []*api.BlockMetadata{{ByteOffset: 4096, SizeBytes: 8192}},
	}
	if err := args.Emitter.SnapshotMetadataIteratorRecord(0, first); err != nil {
		t.Fatalf("first record error = %v", err)
	}
	if err := args.Emitter.SnapshotMetadataIteratorRecord(1, second); err != nil {
		t.Fatalf("second record error = %v", err)
	}
	if err := args.Emitter.SnapshotMetadataIteratorDone(2); err != nil {
		t.Fatalf("done error = %v", err)
	}

	if len(result.Blocks) != 2 {
		t.Fatalf("result has %d blocks, want 2", len(result.Blocks))
	}
	if result.Blocks[0] != block(0, 4096) || result.Blocks[1] != block(4096, 8192) {
		t.Errorf("result blocks = %+v, want [{0 4096} {4096 8192}]", result.Blocks)
	}
	if result.VolumeCapacityBytes != 2<<30 {
		t.Errorf("VolumeCapacityBytes = %d, want %d", result.VolumeCapacityBytes, int64(2<<30))
	}
	if result.BlockMetadataType != api.BlockMetadataType_VARIABLE_LENGTH {
		t.Errorf("BlockMetadataType = %v, want VARIABLE_LENGTH", result.BlockMetadataType)
	}
}

func TestNewClientInvalidTLSConfig(t *testing.T) {
	_, err := NewClient(&rest.Config{
		Host:            "https://example.invalid",
		TLSClientConfig: rest.TLSClientConfig{CertFile: filepath.Join(t.TempDir(), "missing.crt")},
	}, "test-ns", "sa-ns", "sa")
	if err == nil {
		t.Fatal("NewClient() with a missing client certificate returned no error")
	}
	if !strings.Contains(err.Error(), "build iterator clients") {
		t.Errorf("NewClient() error = %v, want it to name the client-building step", err)
	}
}

// A cancelled context makes every call fail before any network I/O, so the
// validation, args wiring, and error wrapping can be checked deterministically.
func TestMethodsWrapCallErrors(t *testing.T) {
	c, err := NewClient(&rest.Config{Host: "https://example.invalid"}, "test-ns", "sa-ns", "sa")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		want string
		call func(context.Context) (*MetadataResult, error)
	}{
		{"allocated", "GetMetadataAllocated", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetAllocatedBlocks(ctx, "snap")
		}},
		{"allocated with options", "GetMetadataAllocated", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetAllocatedBlocksWithOptions(ctx, "snap", 4096, 10)
		}},
		{"delta by name", "GetMetadataDelta", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetChangedBlocks(ctx, "base", "snap")
		}},
		{"delta by name with options", "GetMetadataDelta", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetChangedBlocksWithOptions(ctx, "base", "snap", 4096, 10)
		}},
		{"delta by ID", "GetMetadataDelta (ID)", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetChangedBlocksByID(ctx, "handle", "snap")
		}},
		{"delta by ID with options", "GetMetadataDelta (ID)", func(ctx context.Context) (*MetadataResult, error) {
			return c.GetChangedBlocksByIDWithOptions(ctx, "handle", "snap", 4096, 10)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.call(ctx)
			if err == nil {
				t.Fatal("expected an error from a cancelled context")
			}
			if result != nil {
				t.Errorf("result = %+v, want nil", result)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to identify %q", err, tc.want)
			}
		})
	}
}

func TestTotalChangedBytes(t *testing.T) {
	tests := []struct {
		name   string
		blocks []BlockMetadata
		want   int64
	}{
		{name: "empty", want: 0},
		{name: "single", blocks: []BlockMetadata{block(0, 4096)}, want: 4096},
		{name: "multiple", blocks: []BlockMetadata{block(0, 4096), block(8192, 8192)}, want: 12288},
		{name: "zero-size block", blocks: []BlockMetadata{block(0, 0)}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(tc.blocks...).TotalChangedBytes()
			if got != tc.want {
				t.Errorf("TotalChangedBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestContainsOffset(t *testing.T) {
	tests := []struct {
		name   string
		blocks []BlockMetadata
		offset int64
		want   bool
	}{
		{name: "empty result", offset: 0, want: false},
		{name: "hit: start of block", blocks: []BlockMetadata{block(4096, 4096)}, offset: 4096, want: true},
		{name: "hit: middle of block", blocks: []BlockMetadata{block(4096, 4096)}, offset: 5000, want: true},
		{name: "hit: last byte of block", blocks: []BlockMetadata{block(4096, 4096)}, offset: 8191, want: true},
		{name: "miss: before block", blocks: []BlockMetadata{block(4096, 4096)}, offset: 4095, want: false},
		{name: "miss: after block", blocks: []BlockMetadata{block(4096, 4096)}, offset: 8192, want: false},
		{name: "miss: offset zero, block non-zero", blocks: []BlockMetadata{block(4096, 4096)}, offset: 0, want: false},
		{name: "zero-size block does not contain its own offset", blocks: []BlockMetadata{block(4096, 0)}, offset: 4096, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(tc.blocks...).ContainsOffset(tc.offset)
			if got != tc.want {
				t.Errorf("ContainsOffset(%d) = %v, want %v", tc.offset, got, tc.want)
			}
		})
	}
}

func TestBlocksAreSorted(t *testing.T) {
	tests := []struct {
		name   string
		blocks []BlockMetadata
		want   bool
	}{
		{name: "empty", want: true},
		{name: "single", blocks: []BlockMetadata{block(0, 4096)}, want: true},
		{name: "sorted ascending", blocks: []BlockMetadata{block(0, 4096), block(4096, 4096), block(16384, 512)}, want: true},
		{name: "unsorted", blocks: []BlockMetadata{block(8192, 4096), block(0, 4096)}, want: false},
		{name: "duplicate offset", blocks: []BlockMetadata{block(4096, 512), block(4096, 512)}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(tc.blocks...).BlocksAreSorted()
			if got != tc.want {
				t.Errorf("BlocksAreSorted() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlocksAreNonOverlapping(t *testing.T) {
	tests := []struct {
		name   string
		blocks []BlockMetadata
		want   bool
	}{
		{name: "empty", want: true},
		{name: "single", blocks: []BlockMetadata{block(0, 4096)}, want: true},
		{name: "adjacent blocks", blocks: []BlockMetadata{block(0, 4096), block(4096, 4096)}, want: true},
		{name: "gapped blocks", blocks: []BlockMetadata{block(0, 512), block(4096, 512)}, want: true},
		{name: "overlapping blocks", blocks: []BlockMetadata{block(0, 8192), block(4096, 4096)}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(tc.blocks...).BlocksAreNonOverlapping()
			if got != tc.want {
				t.Errorf("BlocksAreNonOverlapping() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContainsOffset_MultiBlock verifies that ContainsOffset matches in any block,
// not just the first one.
func TestContainsOffset_MultiBlock(t *testing.T) {
	tests := []struct {
		name   string
		blocks []BlockMetadata
		offset int64
		want   bool
	}{
		{
			name:   "hit: second of two blocks",
			blocks: []BlockMetadata{block(0, 512), block(4096, 4096)},
			offset: 4096,
			want:   true,
		},
		{
			name:   "hit: middle of second block",
			blocks: []BlockMetadata{block(0, 512), block(4096, 4096)},
			offset: 5000,
			want:   true,
		},
		{
			name:   "hit: third of three blocks",
			blocks: []BlockMetadata{block(0, 512), block(4096, 512), block(16384, 4096)},
			offset: 16384,
			want:   true,
		},
		{
			name:   "miss: falls between two blocks",
			blocks: []BlockMetadata{block(0, 512), block(4096, 512)},
			offset: 1000,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(tc.blocks...).ContainsOffset(tc.offset)
			if got != tc.want {
				t.Errorf("ContainsOffset(%d) = %v, want %v", tc.offset, got, tc.want)
			}
		})
	}
}

// TestContainsOffset_BinarySearch verifies that ContainsOffset correctly handles
// edge cases that exercise the binary-search boundary conditions: offset at the
// last byte of a block (end-exclusive), offset in a gap between many blocks, and
// hits at the very end of a large result set.
func TestContainsOffset_BinarySearch(t *testing.T) {
	// Build a sorted sequence of non-overlapping blocks to stress the search.
	// Blocks: [0,4096), [8192,12288), [16384,20480), [32768,36864), [65536,69632)
	blocks := []BlockMetadata{
		block(0, 4096),
		block(8192, 4096),
		block(16384, 4096),
		block(32768, 4096),
		block(65536, 4096),
	}
	tests := []struct {
		name   string
		offset int64
		want   bool
	}{
		{"hit: first byte of first block", 0, true},
		{"hit: last byte of first block", 4095, true},
		{"miss: first byte past first block", 4096, false},
		{"miss: gap between first and second block", 6000, false},
		{"hit: first byte of second block", 8192, true},
		{"hit: last byte of last block", 69631, true},
		{"miss: first byte past last block", 69632, false},
		{"miss: before all blocks (negative would wrap, but offset after all)", 100000, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := makeResult(blocks...).ContainsOffset(tc.offset)
			if got != tc.want {
				t.Errorf("ContainsOffset(%d) = %v, want %v", tc.offset, got, tc.want)
			}
		})
	}
}

// TestCollectingEmitter_MultiRecord verifies that BlockMetadataType and
// VolumeCapacityBytes are both updated on every record, not just the first.
func TestCollectingEmitter_MultiRecord(t *testing.T) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	records := []iterator.IteratorMetadata{
		{
			BlockMetadataType:   BlockMetadataTypeAllocated,
			VolumeCapacityBytes: 1073741824,
		},
		{
			BlockMetadataType:   BlockMetadataTypeAllocated,
			VolumeCapacityBytes: 1073741824,
		},
	}

	for i, rec := range records {
		if err := emitter.SnapshotMetadataIteratorRecord(i, rec); err != nil {
			t.Fatalf("record %d: unexpected error: %v", i, err)
		}
	}

	if result.BlockMetadataType != BlockMetadataTypeAllocated {
		t.Errorf("BlockMetadataType = %v, want VARIABLE_LENGTH", result.BlockMetadataType)
	}
	if result.VolumeCapacityBytes != 1073741824 {
		t.Errorf("VolumeCapacityBytes = %d, want 1073741824", result.VolumeCapacityBytes)
	}
}

// TestCollectingEmitter_BlocksAccumulated verifies that blocks from multiple
// streaming records are all appended to the result, not replaced.
func TestCollectingEmitter_BlocksAccumulated(t *testing.T) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	records := []iterator.IteratorMetadata{
		{
			BlockMetadataType:   BlockMetadataTypeAllocated,
			VolumeCapacityBytes: 1073741824,
			BlockMetadata: []*api.BlockMetadata{
				{ByteOffset: 0, SizeBytes: 4096},
				{ByteOffset: 8192, SizeBytes: 4096},
			},
		},
		{
			BlockMetadataType:   BlockMetadataTypeAllocated,
			VolumeCapacityBytes: 1073741824,
			BlockMetadata: []*api.BlockMetadata{
				{ByteOffset: 16384, SizeBytes: 4096},
			},
		},
	}

	for i, rec := range records {
		if err := emitter.SnapshotMetadataIteratorRecord(i, rec); err != nil {
			t.Fatalf("record %d: unexpected error: %v", i, err)
		}
	}

	if len(result.Blocks) != 3 {
		t.Fatalf("expected 3 accumulated blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].ByteOffset != 0 {
		t.Errorf("block[0].ByteOffset = %d, want 0", result.Blocks[0].ByteOffset)
	}
	if result.Blocks[1].ByteOffset != 8192 {
		t.Errorf("block[1].ByteOffset = %d, want 8192", result.Blocks[1].ByteOffset)
	}
	if result.Blocks[2].ByteOffset != 16384 {
		t.Errorf("block[2].ByteOffset = %d, want 16384", result.Blocks[2].ByteOffset)
	}
}

// TestCollectingEmitter_LastVolumeCapacityWins documents that VolumeCapacityBytes
// from the last record overwrites earlier values (last-write-wins).
func TestCollectingEmitter_LastVolumeCapacityWins(t *testing.T) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	records := []iterator.IteratorMetadata{
		{BlockMetadataType: BlockMetadataTypeAllocated, VolumeCapacityBytes: 1073741824},
		{BlockMetadataType: BlockMetadataTypeAllocated, VolumeCapacityBytes: 2147483648},
	}
	for i, rec := range records {
		if err := emitter.SnapshotMetadataIteratorRecord(i, rec); err != nil {
			t.Fatalf("record %d: unexpected error: %v", i, err)
		}
	}

	if result.VolumeCapacityBytes != 2147483648 {
		t.Errorf("VolumeCapacityBytes = %d, want last record's value 2147483648", result.VolumeCapacityBytes)
	}
}

// TestCollectingEmitter_Done verifies SnapshotMetadataIteratorDone returns nil.
func TestCollectingEmitter_Done(t *testing.T) {
	emitter := &collectingEmitter{result: &MetadataResult{}}
	if err := emitter.SnapshotMetadataIteratorDone(0); err != nil {
		t.Errorf("SnapshotMetadataIteratorDone() returned unexpected error: %v", err)
	}
}

func TestClientRejectsEmptyNames(t *testing.T) {
	// Validation runs before any gRPC call, so a zero-value Client is sufficient.
	c := &Client{}
	ctx := context.Background()

	tests := []struct {
		name string
		call func() (*MetadataResult, error)
	}{
		{"GetAllocatedBlocks empty snapshot", func() (*MetadataResult, error) { return c.GetAllocatedBlocks(ctx, "") }},
		{"GetChangedBlocks empty prev", func() (*MetadataResult, error) { return c.GetChangedBlocks(ctx, "", "snap") }},
		{"GetChangedBlocks empty snapshot", func() (*MetadataResult, error) { return c.GetChangedBlocks(ctx, "prev", "") }},
		{"GetChangedBlocksByID empty prev ID", func() (*MetadataResult, error) { return c.GetChangedBlocksByID(ctx, "", "snap") }},
		{"GetChangedBlocksByID empty snapshot", func() (*MetadataResult, error) { return c.GetChangedBlocksByID(ctx, "id", "") }},
		{"GetAllocatedBlocksWithOptions empty snapshot", func() (*MetadataResult, error) {
			return c.GetAllocatedBlocksWithOptions(ctx, "", 0, 10)
		}},
		{"GetChangedBlocksWithOptions empty prev", func() (*MetadataResult, error) {
			return c.GetChangedBlocksWithOptions(ctx, "", "snap", 0, 10)
		}},
		{"GetChangedBlocksWithOptions empty snapshot", func() (*MetadataResult, error) {
			return c.GetChangedBlocksWithOptions(ctx, "prev", "", 0, 10)
		}},
		{"GetChangedBlocksByIDWithOptions empty prev ID", func() (*MetadataResult, error) {
			return c.GetChangedBlocksByIDWithOptions(ctx, "", "snap", 0, 10)
		}},
		{"GetChangedBlocksByIDWithOptions empty snapshot", func() (*MetadataResult, error) {
			return c.GetChangedBlocksByIDWithOptions(ctx, "id", "", 0, 10)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.call()
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if res != nil {
				t.Errorf("expected nil result, got %+v", res)
			}
		})
	}
}
