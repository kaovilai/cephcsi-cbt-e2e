// Minimal CLI to call GetMetadataAllocated or GetMetadataDelta on an existing VolumeSnapshot.
//
// Usage (allocated blocks):
//
//	cbt-check -namespace <ns> -snapshot <name>
//
// Usage (changed blocks / delta by snapshot name):
//
//	cbt-check -namespace <ns> -snapshot <name> -prev-snapshot <base-name>
//
// Usage (changed blocks / delta by CSI snapshot handle):
//
//	cbt-check -namespace <ns> -snapshot <name> -prev-snapshot-id <csi-handle>
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	namespace := flag.String("namespace", "", "VolumeSnapshot namespace")
	snapshot := flag.String("snapshot", "", "VolumeSnapshot name (target/newer snapshot)")
	prevSnapshot := flag.String("prev-snapshot", "", "Base VolumeSnapshot name for delta (omit for allocated-blocks mode)")
	prevSnapshotID := flag.String("prev-snapshot-id", "", "CSI snapshot handle of base snapshot for delta (alternative to -prev-snapshot; allows delta even if base VolumeSnapshot is deleted)")
	saNamespace := flag.String("sa-namespace", "", "ServiceAccount namespace (defaults to -namespace)")
	saName := flag.String("sa-name", "cbt-client", "ServiceAccount name")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig (uses in-cluster config, then ~/.kube/config, if empty)")
	timeout := flag.Duration("timeout", 60*time.Second, "Timeout for CBT API calls")
	flag.Parse()

	if *namespace == "" || *snapshot == "" {
		fmt.Fprintln(os.Stderr, "Usage: cbt-check -namespace <ns> -snapshot <name> [-prev-snapshot <base>] [-prev-snapshot-id <csi-handle>] [-sa-namespace <ns>] [-sa-name <sa>] [-timeout <duration>]")
		os.Exit(1)
	}
	if *prevSnapshot != "" && *prevSnapshotID != "" {
		fmt.Fprintln(os.Stderr, "Error: -prev-snapshot and -prev-snapshot-id are mutually exclusive")
		os.Exit(1)
	}
	if *saNamespace == "" {
		*saNamespace = *namespace
	}

	var config *rest.Config
	var err error
	if *kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		// Try in-cluster config first, then fall back to default kubeconfig
		// (respects KUBECONFIG env var and ~/.kube/config).
		config, err = rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
	}
	if err != nil {
		log.Fatalf("Failed to get kubeconfig: %v", err)
	}

	client, err := cbt.NewClient(config, *namespace, *saNamespace, *saName)
	if err != nil {
		log.Fatalf("Failed to create CBT client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var result *cbt.MetadataResult
	switch {
	case *prevSnapshotID != "":
		log.Printf("Calling GetChangedBlocksByID(namespace=%s, prevID=%s, snapshot=%s, sa=%s/%s)",
			*namespace, *prevSnapshotID, *snapshot, *saNamespace, *saName)
		result, err = client.GetChangedBlocksByID(ctx, *prevSnapshotID, *snapshot)
		if err != nil {
			log.Fatalf("GetChangedBlocksByID failed: %v", err)
		}
	case *prevSnapshot != "":
		log.Printf("Calling GetChangedBlocks(namespace=%s, prev=%s, snapshot=%s, sa=%s/%s)",
			*namespace, *prevSnapshot, *snapshot, *saNamespace, *saName)
		result, err = client.GetChangedBlocks(ctx, *prevSnapshot, *snapshot)
		if err != nil {
			log.Fatalf("GetChangedBlocks failed: %v", err)
		}
	default:
		log.Printf("Calling GetAllocatedBlocks(namespace=%s, snapshot=%s, sa=%s/%s)",
			*namespace, *snapshot, *saNamespace, *saName)
		result, err = client.GetAllocatedBlocks(ctx, *snapshot)
		if err != nil {
			log.Fatalf("GetAllocatedBlocks failed: %v", err)
		}
	}

	log.Printf("Result: %d blocks, totalBytes=%d, volumeCapacity=%d, type=%v",
		len(result.Blocks), result.TotalChangedBytes(), result.VolumeCapacityBytes, result.BlockMetadataType)
	for i, b := range result.Blocks {
		log.Printf("  block[%d]: offset=%d size=%d", i, b.ByteOffset, b.SizeBytes)
	}
	if len(result.Blocks) == 0 {
		// 0 blocks is a valid CBT result (e.g. snapshot of an unwritten device).
		// Print a notice but exit 0 — the API call succeeded.
		log.Println("NOTICE: 0 blocks returned — snapshot may be empty or unwritten")
	}
}
