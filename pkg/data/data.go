// Package data provides known-pattern writing and verification for block-level
// accuracy testing of CBT metadata.
package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
	"github.com/cephcsi-cbt-e2e/pkg/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// DefaultBlockSize is the default CBT block size (1 MiB).
	DefaultBlockSize = 1024 * 1024
	// DefaultDevicePath is the default block device path in pods.
	DefaultDevicePath = "/dev/xvda"
)

// WriteKnownPattern writes a known byte pattern at a specific offset on a block device.
// This is used to verify that CBT correctly reports which blocks have been written.
func WriteKnownPattern(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName string, offset int64, sizeBytes int64, pattern byte) error {

	// Use dd to write a known pattern at the specified offset.
	// conv=fsync ensures data is flushed to the block device before dd returns,
	// which is critical for RBD snapshots to capture the written data.
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf(
			"dd if=/dev/zero bs=1 count=%d 2>/dev/null | tr '\\0' '\\x%02x' | dd of=%s bs=1 seek=%d conv=notrunc,fsync 2>/dev/null",
			sizeBytes, pattern, DefaultDevicePath, offset,
		),
	}

	_, stderr, err := k8s.ExecInPod(ctx, clientset, config, namespace, podName, "", cmd)
	if err != nil {
		return fmt.Errorf("failed to write pattern at offset %d: %s: %w", offset, stderr, err)
	}

	return nil
}

// WriteBlockPattern writes a unique pattern to a specific 1MiB block by block index.
func WriteBlockPattern(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName string, blockIndex int, pattern byte) error {

	offset := int64(blockIndex) * DefaultBlockSize
	return WriteKnownPattern(ctx, clientset, config, namespace, podName, offset, DefaultBlockSize, pattern)
}

// ReadBlockHash reads a block and returns its SHA256 hash, for verification.
func ReadBlockHash(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName string, offset int64, sizeBytes int64) (string, error) {

	cmd := []string{
		"sh", "-c",
		fmt.Sprintf(
			"dd if=%s bs=1 skip=%d count=%d 2>/dev/null | sha256sum | cut -d' ' -f1",
			DefaultDevicePath, offset, sizeBytes,
		),
	}

	stdout, stderr, err := k8s.ExecInPod(ctx, clientset, config, namespace, podName, "", cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read block at offset %d: %s: %w", offset, stderr, err)
	}

	return strings.TrimSpace(stdout), nil
}

// VerifyAllocatedBlocks checks that all blocks reported by CBT as allocated
// correspond to blocks that actually contain written data (non-zero).
func VerifyAllocatedBlocks(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName string, result *cbt.MetadataResult) error {

	zeroHash := zeroBlockHash(DefaultBlockSize)

	for _, block := range result.Blocks {
		hash, err := ReadBlockHash(ctx, clientset, config, namespace, podName, block.ByteOffset, block.SizeBytes)
		if err != nil {
			return fmt.Errorf("failed to read block at offset %d: %w", block.ByteOffset, err)
		}

		if hash == zeroHash {
			return fmt.Errorf("block at offset %d reported as allocated but contains only zeros", block.ByteOffset)
		}
	}

	return nil
}

// VerifyChangedBlocks checks that all blocks reported by CBT as changed
// between two snapshots actually differ in content between the two pods.
func VerifyChangedBlocks(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, basePod, targetPod string, result *cbt.MetadataResult) error {

	for _, block := range result.Blocks {
		baseHash, err := ReadBlockHash(ctx, clientset, config, namespace, basePod, block.ByteOffset, block.SizeBytes)
		if err != nil {
			return fmt.Errorf("failed to read base block at offset %d: %w", block.ByteOffset, err)
		}

		targetHash, err := ReadBlockHash(ctx, clientset, config, namespace, targetPod, block.ByteOffset, block.SizeBytes)
		if err != nil {
			return fmt.Errorf("failed to read target block at offset %d: %w", block.ByteOffset, err)
		}

		if baseHash == targetHash {
			return fmt.Errorf("block at offset %d reported as changed but content is identical", block.ByteOffset)
		}
	}

	return nil
}

// VerifyUnchangedBlocksNotReported checks that blocks NOT in the delta result
// are actually unchanged between base and target.
func VerifyUnchangedBlocksNotReported(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, basePod, targetPod string, result *cbt.MetadataResult, volumeSize int64) error {

	numBlocks := int(volumeSize / DefaultBlockSize)
	for i := 0; i < numBlocks; i++ {
		offset := int64(i) * DefaultBlockSize
		if result.ContainsOffset(offset) {
			continue // This block was reported as changed, skip
		}

		baseHash, err := ReadBlockHash(ctx, clientset, config, namespace, basePod, offset, DefaultBlockSize)
		if err != nil {
			continue // Some blocks may not be readable
		}

		targetHash, err := ReadBlockHash(ctx, clientset, config, namespace, targetPod, offset, DefaultBlockSize)
		if err != nil {
			continue
		}

		if baseHash != targetHash {
			return fmt.Errorf("block at offset %d NOT reported as changed but content differs", offset)
		}
	}

	return nil
}

// WriteFile writes content to a file on a Filesystem-mode PVC mounted at /mnt/data.
func WriteFile(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName, filename, content string) error {

	path := fmt.Sprintf("/mnt/data/%s", filename)
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf("printf '%%s' '%s' > %s && sync", content, path),
	}

	_, stderr, err := k8s.ExecInPod(ctx, clientset, config, namespace, podName, "", cmd)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %s: %w", path, stderr, err)
	}
	return nil
}

// ReadFile reads a file from a Filesystem-mode PVC mounted at /mnt/data.
func ReadFile(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName, filename string) (string, error) {

	path := fmt.Sprintf("/mnt/data/%s", filename)
	cmd := []string{"cat", path}

	stdout, stderr, err := k8s.ExecInPod(ctx, clientset, config, namespace, podName, "", cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %s: %w", path, stderr, err)
	}
	return stdout, nil
}

// ReadFileHash returns the SHA256 hash of a file on a Filesystem-mode PVC.
func ReadFileHash(ctx context.Context, clientset kubernetes.Interface, config *rest.Config,
	namespace, podName, filename string) (string, error) {

	path := fmt.Sprintf("/mnt/data/%s", filename)
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf("sha256sum %s | cut -d' ' -f1", path),
	}

	stdout, stderr, err := k8s.ExecInPod(ctx, clientset, config, namespace, podName, "", cmd)
	if err != nil {
		return "", fmt.Errorf("failed to hash file %s: %s: %w", path, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

// zeroBlockHash returns the SHA256 hash of a block of all zeros.
func zeroBlockHash(size int64) string {
	h := sha256.New()
	zeros := make([]byte, size)
	h.Write(zeros)
	return hex.EncodeToString(h.Sum(nil))
}
