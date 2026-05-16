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
