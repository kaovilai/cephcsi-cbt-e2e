// Minimal reproducer for go-ceph DiffIterateByID.
// Build inside a container with CGO + librbd-devel:
//   CGO_ENABLED=1 go build -o /tmp/repro ./cmd/go-ceph-repro/
// Run inside cluster with Ceph access:
//   /tmp/repro -pool ocs-storagecluster-cephblockpool \
//     -image csi-vol-XXX -snap-id 21 \
//     -user client.admin -keyring /etc/ceph/ceph.client.admin.keyring
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
)

func main() {
	pool := flag.String("pool", "ocs-storagecluster-cephblockpool", "RBD pool")
	imageName := flag.String("image", "", "Source image name (required)")
	snapID := flag.Uint64("snap-id", 0, "Snap ID to set context to (required)")
	confFile := flag.String("conf", "/etc/ceph/ceph.conf", "Ceph config file")
	user := flag.String("user", "", "Ceph user (e.g. client.admin)")
	keyring := flag.String("keyring", "", "Path to keyring file")
	flag.Parse()

	if *imageName == "" || *snapID == 0 {
		fmt.Fprintln(os.Stderr, "Usage: repro -image <name> -snap-id <id> [-pool <pool>] [-conf <conf>] [-user <user>] [-keyring <keyring>]")
		os.Exit(1)
	}

	// Connect to Ceph
	var conn *rados.Conn
	var err error
	if *user != "" {
		conn, err = rados.NewConnWithUser(*user)
	} else {
		conn, err = rados.NewConn()
	}
	if err != nil {
		log.Fatalf("NewConn: %v", err)
	}
	if err := conn.ReadConfigFile(*confFile); err != nil {
		log.Fatalf("ReadConfigFile: %v", err)
	}
	if *keyring != "" {
		if err := conn.SetConfigOption("keyring", *keyring); err != nil {
			log.Fatalf("SetConfigOption keyring: %v", err)
		}
	}
	if err := conn.Connect(); err != nil {
		log.Fatalf("Connect: %v", err)
	}
	defer conn.Shutdown()
	log.Println("Connected to Ceph")

	ioctx, err := conn.OpenIOContext(*pool)
	if err != nil {
		log.Fatalf("OpenIOContext: %v", err)
	}
	defer ioctx.Destroy()

	// Open image with no snap context
	img, err := rbd.OpenImage(ioctx, *imageName, rbd.NoSnapshot)
	if err != nil {
		log.Fatalf("OpenImage: %v", err)
	}
	defer img.Close()

	// Get image size
	stat, err := img.Stat()
	if err != nil {
		log.Fatalf("Stat: %v", err)
	}
	size := stat.Size
	log.Printf("Image: %s, size: %d bytes (%d MiB)", *imageName, size, size/1024/1024)

	// Set snap by ID
	if err := img.SetSnapByID(*snapID); err != nil {
		log.Fatalf("SetSnapByID(%d): %v", *snapID, err)
	}
	log.Printf("Set snap context to ID %d", *snapID)

	// Test 1: DiffIterate (rbd_diff_iterate2, statically linked)
	log.Println("\n=== Test 1: DiffIterate (rbd_diff_iterate2) ===")
	var blocks2 []struct{ offset, length uint64 }
	diffConfig := rbd.DiffIterateConfig{
		Offset: 0,
		Length: size,
		Callback: func(offset, length uint64, exists int, data interface{}) int {
			blocks2 = append(blocks2, struct{ offset, length uint64 }{offset, length})
			return 0
		},
	}
	if err := img.DiffIterate(diffConfig); err != nil {
		log.Printf("DiffIterate error: %v", err)
	} else {
		log.Printf("DiffIterate: %d blocks found", len(blocks2))
		var total uint64
		for i, b := range blocks2 {
			if i < 5 {
				log.Printf("  block[%d]: offset=%d length=%d", i, b.offset, b.length)
			}
			total += b.length
		}
		log.Printf("  Total bytes: %d", total)
	}

	// Test 2: DiffIterateByID (rbd_diff_iterate3, dlsym)
	log.Println("\n=== Test 2: DiffIterateByID (rbd_diff_iterate3 via dlsym) ===")
	var blocks3 []struct{ offset, length uint64 }
	diffByIDConfig := rbd.DiffIterateByIDConfig{
		FromSnapID: 0,
		Offset:     0,
		Length:     size,
		Callback: func(offset, length uint64, exists int, data interface{}) int {
			log.Printf("  CALLBACK: offset=%d length=%d exists=%d", offset, length, exists)
			blocks3 = append(blocks3, struct{ offset, length uint64 }{offset, length})
			return 0
		},
	}
	log.Printf("Calling DiffIterateByID(FromSnapID=0, Offset=0, Length=%d)", size)
	if err := img.DiffIterateByID(diffByIDConfig); err != nil {
		log.Printf("DiffIterateByID error: %v", err)
	} else {
		log.Printf("DiffIterateByID: %d blocks found", len(blocks3))
		var total uint64
		for _, b := range blocks3 {
			total += b.length
		}
		log.Printf("  Total bytes: %d", total)
	}

	// Compare
	log.Println("\n=== Comparison ===")
	log.Printf("DiffIterate (v2):     %d blocks", len(blocks2))
	log.Printf("DiffIterateByID (v3): %d blocks", len(blocks3))
	if len(blocks2) != len(blocks3) {
		log.Printf("MISMATCH! DiffIterate found %d blocks, DiffIterateByID found %d blocks", len(blocks2), len(blocks3))
		os.Exit(1)
	}
	log.Println("MATCH - both methods return same number of blocks")
}
