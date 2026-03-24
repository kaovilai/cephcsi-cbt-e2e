// Package cbt wraps the external-snapshot-metadata iterator for CBT gRPC calls.
package cbt

import (
	"context"
	"fmt"

	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/api"
	"github.com/kubernetes-csi/external-snapshot-metadata/pkg/iterator"
	"k8s.io/client-go/rest"
)

// BlockMetadata represents a single block's metadata.
type BlockMetadata struct {
	ByteOffset int64
	SizeBytes  int64
}

// MetadataResult holds the collected metadata from a CBT call.
type MetadataResult struct {
	Blocks              []BlockMetadata
	VolumeCapacityBytes int64
	BlockMetadataType   api.BlockMetadataType
}

// Client wraps the external-snapshot-metadata iterator for CBT operations.
type Client struct {
	clients     iterator.Clients
	namespace   string
	saNamespace string
	saName      string
}

// NewClient creates a CBT client from a Kubernetes rest.Config.
// saNamespace and saName specify the ServiceAccount used to create authentication
// tokens for the snapshot metadata gRPC service. When running outside a pod
// (e.g. as kubeadmin), these must be set explicitly because the iterator library
// cannot auto-detect a ServiceAccount from certificate-based identities.
func NewClient(config *rest.Config, namespace, saNamespace, saName string) (*Client, error) {
	clients, err := iterator.BuildClients(config)
	if err != nil {
		return nil, fmt.Errorf("failed to build iterator clients: %w", err)
	}

	return &Client{
		clients:     clients,
		namespace:   namespace,
		saNamespace: saNamespace,
		saName:      saName,
	}, nil
}

// collectingEmitter collects block metadata into a MetadataResult.
type collectingEmitter struct {
	result *MetadataResult
}

func (e *collectingEmitter) SnapshotMetadataIteratorRecord(_ int, metadata iterator.IteratorMetadata) error {
	if len(e.result.Blocks) == 0 {
		e.result.BlockMetadataType = metadata.BlockMetadataType
	}
	e.result.VolumeCapacityBytes = metadata.VolumeCapacityBytes
	for _, bm := range metadata.BlockMetadata {
		e.result.Blocks = append(e.result.Blocks, BlockMetadata{
			ByteOffset: bm.ByteOffset,
			SizeBytes:  bm.SizeBytes,
		})
	}
	return nil
}

func (e *collectingEmitter) SnapshotMetadataIteratorDone(_ int) error {
	return nil
}

// GetAllocatedBlocks returns all allocated blocks for a snapshot.
func (c *Client) GetAllocatedBlocks(ctx context.Context, snapshotName string) (*MetadataResult, error) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	args := iterator.Args{
		Clients:      c.clients,
		Emitter:      emitter,
		Namespace:    c.namespace,
		SnapshotName: snapshotName,
		SANamespace:  c.saNamespace,
		SAName:       c.saName,
	}

	if err := iterator.GetSnapshotMetadata(ctx, args); err != nil {
		return nil, fmt.Errorf("GetMetadataAllocated for %s: %w", snapshotName, err)
	}

	return result, nil
}

// GetChangedBlocks returns blocks changed between two snapshots.
// prevSnapshotName is the base (older) snapshot, snapshotName is the target (newer).
func (c *Client) GetChangedBlocks(ctx context.Context, prevSnapshotName, snapshotName string) (*MetadataResult, error) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	args := iterator.Args{
		Clients:          c.clients,
		Emitter:          emitter,
		Namespace:        c.namespace,
		SnapshotName:     snapshotName,
		PrevSnapshotName: prevSnapshotName,
		SANamespace:      c.saNamespace,
		SAName:           c.saName,
	}

	if err := iterator.GetSnapshotMetadata(ctx, args); err != nil {
		return nil, fmt.Errorf("GetMetadataDelta %s -> %s: %w", prevSnapshotName, snapshotName, err)
	}

	return result, nil
}

// GetChangedBlocksByID returns blocks changed using a CSI snapshot handle as the base.
// This allows delta computation even if the base VolumeSnapshot has been deleted.
func (c *Client) GetChangedBlocksByID(ctx context.Context, prevSnapshotID, snapshotName string) (*MetadataResult, error) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}

	args := iterator.Args{
		Clients:        c.clients,
		Emitter:        emitter,
		Namespace:      c.namespace,
		SnapshotName:   snapshotName,
		PrevSnapshotID: prevSnapshotID,
		SANamespace:    c.saNamespace,
		SAName:         c.saName,
	}

	if err := iterator.GetSnapshotMetadata(ctx, args); err != nil {
		return nil, fmt.Errorf("GetMetadataDelta (ID) %s -> %s: %w", prevSnapshotID, snapshotName, err)
	}

	return result, nil
}

// GetAllocatedBlocksWithOptions returns allocated blocks for a snapshot with pagination options.
func (c *Client) GetAllocatedBlocksWithOptions(ctx context.Context, snapshotName string, startingOffset int64, maxResults int32) (*MetadataResult, error) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}
	args := iterator.Args{
		Clients:        c.clients,
		Emitter:        emitter,
		Namespace:      c.namespace,
		SnapshotName:   snapshotName,
		StartingOffset: startingOffset,
		MaxResults:     maxResults,
		SANamespace:    c.saNamespace,
		SAName:         c.saName,
	}
	if err := iterator.GetSnapshotMetadata(ctx, args); err != nil {
		return nil, fmt.Errorf("GetMetadataAllocated for %s (offset=%d, max=%d): %w", snapshotName, startingOffset, maxResults, err)
	}
	return result, nil
}

// GetChangedBlocksWithOptions returns blocks changed between two snapshots with pagination options.
func (c *Client) GetChangedBlocksWithOptions(ctx context.Context, prevSnapshotName, snapshotName string, startingOffset int64, maxResults int32) (*MetadataResult, error) {
	result := &MetadataResult{}
	emitter := &collectingEmitter{result: result}
	args := iterator.Args{
		Clients:          c.clients,
		Emitter:          emitter,
		Namespace:        c.namespace,
		SnapshotName:     snapshotName,
		PrevSnapshotName: prevSnapshotName,
		StartingOffset:   startingOffset,
		MaxResults:       maxResults,
		SANamespace:      c.saNamespace,
		SAName:           c.saName,
	}
	if err := iterator.GetSnapshotMetadata(ctx, args); err != nil {
		return nil, fmt.Errorf("GetMetadataDelta %s -> %s (offset=%d, max=%d): %w", prevSnapshotName, snapshotName, startingOffset, maxResults, err)
	}
	return result, nil
}

// TotalChangedBytes returns the total byte count across all blocks.
func (r *MetadataResult) TotalChangedBytes() int64 {
	var total int64
	for _, b := range r.Blocks {
		total += b.SizeBytes
	}
	return total
}

// ContainsOffset returns true if any block covers the given byte offset.
func (r *MetadataResult) ContainsOffset(offset int64) bool {
	for _, b := range r.Blocks {
		if offset >= b.ByteOffset && offset < b.ByteOffset+b.SizeBytes {
			return true
		}
	}
	return false
}

// BlocksAreSorted returns true if blocks are in ascending ByteOffset order.
func (r *MetadataResult) BlocksAreSorted() bool {
	for i := 1; i < len(r.Blocks); i++ {
		if r.Blocks[i].ByteOffset <= r.Blocks[i-1].ByteOffset {
			return false
		}
	}
	return true
}

// BlocksAreNonOverlapping returns true if no block ranges overlap.
func (r *MetadataResult) BlocksAreNonOverlapping() bool {
	for i := 1; i < len(r.Blocks); i++ {
		prevEnd := r.Blocks[i-1].ByteOffset + r.Blocks[i-1].SizeBytes
		if r.Blocks[i].ByteOffset < prevEnd {
			return false
		}
	}
	return true
}

// BlockMetadataTypeAllocated is the type for allocated block metadata.
var BlockMetadataTypeAllocated = api.BlockMetadataType_FIXED_LENGTH
