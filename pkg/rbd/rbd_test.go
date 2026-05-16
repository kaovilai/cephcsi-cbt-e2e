package rbd

import (
	"reflect"
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

func TestParseChildrenOutput(t *testing.T) {
tests := []struct {
name   string
output string
want   []string
}{
{
name:   "empty",
output: "",
want:   nil,
},
{
name:   "single child",
output: "rbd/csi-vol-abc123",
want:   []string{"csi-vol-abc123"},
},
{
name:   "multiple children",
output: "rbd/csi-vol-abc123\nrbd/csi-vol-def456\n",
want:   []string{"csi-vol-abc123", "csi-vol-def456"},
},
{
name:   "trailing whitespace",
output: "  rbd/csi-vol-abc123  \n  rbd/csi-vol-def456  \n",
want:   []string{"csi-vol-abc123", "csi-vol-def456"},
},
{
name:   "no pool prefix",
output: "csi-vol-abc123",
want:   []string{"csi-vol-abc123"},
},
{
name:   "blank lines skipped",
output: "rbd/img1\n\nrbd/img2\n",
want:   []string{"img1", "img2"},
},
}
for _, tc := range tests {
t.Run(tc.name, func(t *testing.T) {
got := parseChildrenOutput(tc.output)
if !reflect.DeepEqual(got, tc.want) {
t.Errorf("parseChildrenOutput(%q) = %v, want %v", tc.output, got, tc.want)
}
})
}
}

func TestParseImagesOutput(t *testing.T) {
tests := []struct {
name   string
output string
want   []string
}{
{
name:   "empty",
output: "",
want:   nil,
},
{
name:   "single image",
output: "csi-vol-abc123",
want:   []string{"csi-vol-abc123"},
},
{
name:   "multiple images",
output: "csi-vol-abc123\ncsi-vol-def456\n",
want:   []string{"csi-vol-abc123", "csi-vol-def456"},
},
{
name:   "blank lines skipped",
output: "img1\n\nimg2\n",
want:   []string{"img1", "img2"},
},
{
name:   "whitespace-only lines skipped",
output: "img1\n   \nimg2\n",
want:   []string{"img1", "img2"},
},
}
for _, tc := range tests {
t.Run(tc.name, func(t *testing.T) {
got := parseImagesOutput(tc.output)
if !reflect.DeepEqual(got, tc.want) {
t.Errorf("parseImagesOutput(%q) = %v, want %v", tc.output, got, tc.want)
}
})
}
}

func TestImageNameFromParentRef(t *testing.T) {
tests := []struct {
name      string
parentRef string
want      string
}{
{
name:      "standard pool/image@snap",
parentRef: "rbd/csi-vol-abc123@csi-snap-xyz",
want:      "csi-vol-abc123",
},
{
name:      "no snapshot suffix",
parentRef: "rbd/csi-vol-abc123",
want:      "csi-vol-abc123",
},
{
name:      "no pool prefix",
parentRef: "csi-vol-abc123@csi-snap-xyz",
want:      "",
},
{
name:      "empty string",
parentRef: "",
want:      "",
},
{
name:      "multiple pool path segments",
parentRef: "rbd/pool/csi-vol-abc123@csi-snap-xyz",
want:      "csi-vol-abc123",
},
}
for _, tc := range tests {
t.Run(tc.name, func(t *testing.T) {
got := imageNameFromParentRef(tc.parentRef)
if got != tc.want {
t.Errorf("imageNameFromParentRef(%q) = %q, want %q", tc.parentRef, got, tc.want)
}
})
}
}
