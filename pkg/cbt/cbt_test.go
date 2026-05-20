package cbt

import (
	"testing"

	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/api"
	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/iterator"
)

func makeResult(blocks ...BlockMetadata) *MetadataResult {
	return &MetadataResult{Blocks: blocks}
}

func block(offset, size int64) BlockMetadata {
	return BlockMetadata{ByteOffset: offset, SizeBytes: size}
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
