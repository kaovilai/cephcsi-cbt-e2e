package main

import (
	"strings"
	"testing"
	"time"
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
