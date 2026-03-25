// Minimal CLI to call GetMetadataAllocated on an existing VolumeSnapshot.
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
	snapshot := flag.String("snapshot", "", "VolumeSnapshot name")
	saNamespace := flag.String("sa-namespace", "", "ServiceAccount namespace (defaults to -namespace)")
	saName := flag.String("sa-name", "cbt-client", "ServiceAccount name")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig (uses in-cluster if empty)")
	flag.Parse()

	if *namespace == "" || *snapshot == "" {
		fmt.Fprintln(os.Stderr, "Usage: cbt-check -namespace <ns> -snapshot <name> [-sa-namespace <ns>] [-sa-name <sa>]")
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Printf("Calling GetAllocatedBlocks(namespace=%s, snapshot=%s, sa=%s/%s)",
		*namespace, *snapshot, *saNamespace, *saName)

	result, err := client.GetAllocatedBlocks(ctx, *snapshot)
	if err != nil {
		log.Fatalf("GetAllocatedBlocks failed: %v", err)
	}

	log.Printf("Result: %d blocks, volumeCapacity=%d, type=%v",
		len(result.Blocks), result.VolumeCapacityBytes, result.BlockMetadataType)
	for i, b := range result.Blocks {
		log.Printf("  block[%d]: offset=%d size=%d", i, b.ByteOffset, b.SizeBytes)
	}
	if len(result.Blocks) == 0 {
		log.Println("BUG: 0 blocks returned!")
		os.Exit(1)
	}
}
