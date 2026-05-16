package cbt

import (
	"testing"
)

func makeResult(blocks ...BlockMetadata) *MetadataResult {
	return &MetadataResult{Blocks: blocks}
}

func block(offset, size int64) BlockMetadata {
	return BlockMetadata{ByteOffset: offset, SizeBytes: size}
}

// TotalChangedBytes

func TestTotalChangedBytes_empty(t *testing.T) {
	if got := makeResult().TotalChangedBytes(); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestTotalChangedBytes_single(t *testing.T) {
	if got := makeResult(block(0, 4096)).TotalChangedBytes(); got != 4096 {
		t.Errorf("expected 4096, got %d", got)
	}
}

func TestTotalChangedBytes_multiple(t *testing.T) {
	r := makeResult(block(0, 4096), block(8192, 8192))
	if got := r.TotalChangedBytes(); got != 12288 {
		t.Errorf("expected 12288, got %d", got)
	}
}

// ContainsOffset

func TestContainsOffset_empty(t *testing.T) {
	if makeResult().ContainsOffset(0) {
		t.Error("expected false for empty result")
	}
}

func TestContainsOffset_hit(t *testing.T) {
	r := makeResult(block(4096, 4096))
	for _, off := range []int64{4096, 5000, 8191} {
		if !r.ContainsOffset(off) {
			t.Errorf("expected offset %d to be contained", off)
		}
	}
}

func TestContainsOffset_miss(t *testing.T) {
	r := makeResult(block(4096, 4096))
	for _, off := range []int64{0, 4095, 8192} {
		if r.ContainsOffset(off) {
			t.Errorf("expected offset %d to not be contained", off)
		}
	}
}

// BlocksAreSorted

func TestBlocksAreSorted_empty(t *testing.T) {
	if !makeResult().BlocksAreSorted() {
		t.Error("expected true for empty result")
	}
}

func TestBlocksAreSorted_single(t *testing.T) {
	if !makeResult(block(0, 4096)).BlocksAreSorted() {
		t.Error("expected true for single block")
	}
}

func TestBlocksAreSorted_sorted(t *testing.T) {
	r := makeResult(block(0, 4096), block(4096, 4096), block(16384, 512))
	if !r.BlocksAreSorted() {
		t.Error("expected sorted blocks to return true")
	}
}

func TestBlocksAreSorted_unsorted(t *testing.T) {
	r := makeResult(block(8192, 4096), block(0, 4096))
	if r.BlocksAreSorted() {
		t.Error("expected unsorted blocks to return false")
	}
}

func TestBlocksAreSorted_duplicateOffset(t *testing.T) {
	r := makeResult(block(4096, 512), block(4096, 512))
	if r.BlocksAreSorted() {
		t.Error("expected duplicate offsets to return false")
	}
}

// BlocksAreNonOverlapping

func TestBlocksAreNonOverlapping_empty(t *testing.T) {
	if !makeResult().BlocksAreNonOverlapping() {
		t.Error("expected true for empty result")
	}
}

func TestBlocksAreNonOverlapping_noOverlap(t *testing.T) {
	r := makeResult(block(0, 4096), block(4096, 4096))
	if !r.BlocksAreNonOverlapping() {
		t.Error("expected adjacent blocks to be non-overlapping")
	}
}

func TestBlocksAreNonOverlapping_overlap(t *testing.T) {
	r := makeResult(block(0, 8192), block(4096, 4096))
	if r.BlocksAreNonOverlapping() {
		t.Error("expected overlapping blocks to return false")
	}
}

func TestBlocksAreNonOverlapping_gapped(t *testing.T) {
	r := makeResult(block(0, 512), block(4096, 512))
	if !r.BlocksAreNonOverlapping() {
		t.Error("expected gapped blocks to be non-overlapping")
	}
}
