package rbd

import (
	"testing"
)

func TestParseCephMajorVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name:  "quincy 17",
			input: "ceph version 17.2.6 (810db68029296a72ef3cff2443e59c810b66b0a2) quincy (stable)",
			want:  17,
		},
		{
			name:  "reef 18",
			input: "ceph version 18.2.4 (e7ad5345525c7aa95470c26863873b581076945d) reef (stable)",
			want:  18,
		},
		{
			name:  "squid 19",
			input: "ceph version 19.0.0 (deadbeef) squid (dev)",
			want:  19,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "too short",
			input:   "ceph version",
			wantErr: true,
		},
		{
			name:    "non-numeric major",
			input:   "ceph version x.2.6 (abc) quincy (stable)",
			wantErr: true,
		},
		{
			name:  "tentacle 20",
			input: "ceph version 20.0.0 (abc123) tentacle (dev)",
			want:  20,
		},
		{
			name:  "no patch version",
			input: "ceph version 17 (abc) quincy (stable)",
			want:  17,
		},
		{
			name:    "only two words",
			input:   "ceph 17",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCephMajorVersion(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tc.want {
				t.Errorf("expected %d, got %d", tc.want, got)
			}
		})
	}
}
