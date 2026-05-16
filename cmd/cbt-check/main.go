// Minimal CLI to call GetMetadataAllocated or GetMetadataDelta on an existing VolumeSnapshot.
//
// Usage (allocated blocks):
//
//	cbt-check -namespace <ns> -snapshot <name>
//
// Usage (changed blocks / delta):
//
//	cbt-check -namespace <ns> -snapshot <name> -prev-snapshot <base-name>
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
	saNamespace := flag.String("sa-namespace", "", "ServiceAccount namespace (defaults to -namespace)")
	saName := flag.String("sa-name", "cbt-client", "ServiceAccount name")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig (uses in-cluster if empty)")
	timeout := flag.Duration("timeout", 60*time.Second, "Timeout for CBT API calls")
	flag.Parse()

	if *namespace == "" || *snapshot == "" {
		fmt.Fprintln(os.Stderr, "Usage: cbt-check -namespace <ns> -snapshot <name> [-prev-snapshot <base>] [-sa-namespace <ns>] [-sa-name <sa>] [-timeout <duration>]")
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
		config, err = rest.InClusterConfig()
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
	if *prevSnapshot != "" {
		log.Printf("Calling GetChangedBlocks(namespace=%s, prev=%s, snapshot=%s, sa=%s/%s)",
			*namespace, *prevSnapshot, *snapshot, *saNamespace, *saName)
		result, err = client.GetChangedBlocks(ctx, *prevSnapshot, *snapshot)
		if err != nil {
			log.Fatalf("GetChangedBlocks failed: %v", err)
		}
	} else {
		log.Printf("Calling GetAllocatedBlocks(namespace=%s, snapshot=%s, sa=%s/%s)",
			*namespace, *snapshot, *saNamespace, *saName)
		result, err = client.GetAllocatedBlocks(ctx, *snapshot)
		if err != nil {
			log.Fatalf("GetAllocatedBlocks failed: %v", err)
		}
	}

	log.Printf("Result: %d blocks, volumeCapacity=%d, type=%v",
		len(result.Blocks), result.VolumeCapacityBytes, result.BlockMetadataType)
	for i, b := range result.Blocks {
		log.Printf("  block[%d]: offset=%d size=%d", i, b.ByteOffset, b.SizeBytes)
	}
	if len(result.Blocks) == 0 {
		log.Println("WARNING: 0 blocks returned")
		os.Exit(1)
	}
}
