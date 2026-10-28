package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
)

func TestValidateFlags(t *testing.T) {
	const (
		ns   = "test-ns"
		snap = "snap"
	)

	tests := []struct {
		name        string
		namespace   string
		snapshot    string
		prev        string
		prevID      string
		timeout     time.Duration
		wantErrPart string
	}{
		{name: "allocated mode", namespace: ns, snapshot: snap, timeout: time.Minute},
		{name: "delta by name", namespace: ns, snapshot: snap, prev: "base", timeout: time.Minute},
		{name: "delta by handle", namespace: ns, snapshot: snap, prevID: "handle", timeout: time.Minute},
		{name: "missing namespace", snapshot: snap, timeout: time.Minute, wantErrPart: "-namespace is required"},
		{name: "missing snapshot", namespace: ns, timeout: time.Minute, wantErrPart: "-snapshot is required"},
		{name: "both delta bases", namespace: ns, snapshot: snap, prev: "base", prevID: "handle", timeout: time.Minute, wantErrPart: "mutually exclusive"},
		{name: "zero timeout", namespace: ns, snapshot: snap, timeout: 0, wantErrPart: "-timeout must be positive"},
		{name: "negative timeout", namespace: ns, snapshot: snap, timeout: -time.Second, wantErrPart: "-timeout must be positive"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFlags(tc.namespace, tc.snapshot, tc.prev, tc.prevID, tc.timeout)
			if tc.wantErrPart == "" {
				if err != nil {
					t.Fatalf("validateFlags() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateFlags() = nil, want error containing %q", tc.wantErrPart)
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Errorf("validateFlags() error = %v, want it to contain %q", err, tc.wantErrPart)
			}
		})
	}
}

func TestBlockLines(t *testing.T) {
	blocks := make([]cbt.BlockMetadata, 30)
	for i := range blocks {
		blocks[i] = cbt.BlockMetadata{ByteOffset: int64(i) * 1024, SizeBytes: 1024}
	}

	tests := []struct {
		name      string
		blocks    []cbt.BlockMetadata
		max       int
		wantLines int
		wantLast  string
	}{
		{name: "empty", max: 20, wantLines: 0},
		{name: "fewer than max", blocks: blocks[:3], max: 20, wantLines: 3},
		{name: "exactly max", blocks: blocks[:2], max: 2, wantLines: 2},
		{name: "truncated", blocks: blocks, max: 2, wantLines: 3, wantLast: "... 28 more blocks"},
		{name: "negative prints all", blocks: blocks, max: -1, wantLines: 30},
		{name: "zero prints only the summary", blocks: blocks, max: 0, wantLines: 1, wantLast: "... 30 more blocks"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := blockLines(tc.blocks, tc.max)
			if len(lines) != tc.wantLines {
				t.Fatalf("blockLines() returned %d lines, want %d: %v", len(lines), tc.wantLines, lines)
			}
			if tc.wantLast != "" && !strings.Contains(lines[len(lines)-1], tc.wantLast) {
				t.Errorf("last line = %q, want it to contain %q", lines[len(lines)-1], tc.wantLast)
			}
		})
	}

	lines := blockLines(blocks[:1], -1)
	if want := "offset=0 size=1024"; !strings.Contains(lines[0], want) {
		t.Errorf("first line = %q, want it to contain %q", lines[0], want)
	}
}
