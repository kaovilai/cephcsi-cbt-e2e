package e2e_test

import "time"

// Timeout constants for Wait* helpers, centralising all tuning in one place.
const (
	// pvcPodReadyTimeout is used when waiting for a PVC to bind or a pod to reach Running.
	pvcPodReadyTimeout = 2 * time.Minute
	// snapshotReadyTimeout is used when waiting for a VolumeSnapshot to become ready.
	snapshotReadyTimeout = 3 * time.Minute
	// longOperationTimeout is used for slower operations: namespace deletion, PVC resize,
	// VolumeSnapshotContent deletion.
	longOperationTimeout = 5 * time.Minute
)
