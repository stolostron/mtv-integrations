// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func vmiWithCPUAndMem(t *testing.T, obj map[string]interface{}) *unstructured.Unstructured {
	t.Helper()
	return &unstructured.Unstructured{Object: obj}
}

// ── extractVolumeEntriesFromVMI ──────────────────────────────────────────────

func TestExtractVolumeEntriesFromVMI(t *testing.T) {
	vmi := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"volumeStatus": []interface{}{
				// PVC-backed volume with capacity in pvcInfo.capacity.storage
				map[string]interface{}{
					"name": "datavol",
					"persistentVolumeClaimInfo": map[string]interface{}{
						"claimName": "my-pvc",
						"capacity":  map[string]interface{}{"storage": "10Gi"},
					},
				},
				// PVC-backed volume with size in requests.storage (fallback path)
				map[string]interface{}{
					"name": "datavol2",
					"persistentVolumeClaimInfo": map[string]interface{}{
						"claimName": "my-pvc2",
						"requests":  map[string]interface{}{"storage": "5Gi"},
					},
				},
				// Non-PVC volume (e.g. cloudInitDisk) — must be skipped
				map[string]interface{}{
					"name": "cloudinit",
				},
				// Non-map entry — must be skipped
				"not-a-map",
			},
		},
	}}

	entries := extractVolumeEntriesFromVMI(vmi)
	if assert.Len(t, entries, 2) {
		assert.Equal(t, "datavol", entries[0].name)
		assert.Equal(t, "my-pvc", entries[0].claimName)
		assert.Equal(t, int64(10*(1<<30)), entries[0].sizeBytes)

		assert.Equal(t, "datavol2", entries[1].name)
		assert.Equal(t, "my-pvc2", entries[1].claimName)
		assert.Equal(t, int64(5*(1<<30)), entries[1].sizeBytes)
	}
}

func TestExtractVolumeEntriesFromVMI_Empty(t *testing.T) {
	vmi := &unstructured.Unstructured{Object: map[string]interface{}{}}
	assert.Empty(t, extractVolumeEntriesFromVMI(vmi))
}

// TestExtractVolumeEntriesFromVMI_MalformedSize verifies that a volume whose
// storage-capacity string cannot be parsed is still included in the result
// (with sizeBytes == 0) rather than silently dropped, so the caller can log
// the anomaly and the scorer does not crash on it.
func TestExtractVolumeEntriesFromVMI_MalformedSize(t *testing.T) {
	vmi := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"volumeStatus": []interface{}{
				map[string]interface{}{
					"name": "badvol",
					"persistentVolumeClaimInfo": map[string]interface{}{
						"claimName": "bad-pvc",
						"capacity":  map[string]interface{}{"storage": "not-a-quantity"},
					},
				},
			},
		},
	}}

	entries := extractVolumeEntriesFromVMI(vmi)
	require.Len(t, entries, 1, "malformed-size volume must still be included")
	assert.Equal(t, "badvol", entries[0].name)
	assert.Equal(t, "bad-pvc", entries[0].claimName)
	assert.Equal(t, int64(0), entries[0].sizeBytes, "unparseable size must default to 0")
}

func TestExtractVolumeEntriesFromVMI_NoPVCInfo(t *testing.T) {
	vmi := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"volumeStatus": []interface{}{
				map[string]interface{}{"name": "cloudinit"},
			},
		},
	}}
	assert.Empty(t, extractVolumeEntriesFromVMI(vmi))
}

// ── watchMCVResult ───────────────────────────────────────────────────────────

// mcvWithResult returns an Unstructured that looks like a ManagedClusterView
// whose status.result has been populated.
func mcvWithResult(result map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": managedClusterViewAPIVersion,
		"kind":       managedClusterViewKind,
		"metadata":   map[string]interface{}{"name": "test-mcv", "namespace": "test-cluster"},
		"status": map[string]interface{}{
			"result": result,
		},
	}}
}

// mcvWithProcessingFalse returns an MCV whose Processing condition is False (error path).
func mcvWithProcessingFalse(reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": managedClusterViewAPIVersion,
		"kind":       managedClusterViewKind,
		"metadata":   map[string]interface{}{"name": "test-mcv", "namespace": "test-cluster"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":   "Processing",
					"status": "False",
					"reason": reason,
				},
			},
		},
	}}
}

// injectWatchEvent registers a watch reactor that fires a Modified event for obj after a brief delay.
func injectWatchEvent(fakeClient *dynamicfake.FakeDynamicClient, obj runtime.Object) {
	fw := watch.NewFake()
	fakeClient.PrependWatchReactor("managedclusterviews",
		func(_ k8stesting.Action) (bool, watch.Interface, error) {
			return true, fw, nil
		})
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(obj)
	}()
}

func TestWatchMCVResult_WithResult(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	injectWatchEvent(fakeClient, mcvWithResult(map[string]interface{}{
		keySpec: map[string]interface{}{"storageClassName": "ceph-rbd"},
	}))

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	obj, err := fetcher.watchMCVResult(context.Background(), "test-cluster", "test-mcv")
	require.NoError(t, err)
	sc, _, _ := unstructured.NestedString(obj.Object, keySpec, "storageClassName")
	assert.Equal(t, "ceph-rbd", sc)
}

func TestWatchMCVResult_ProcessingFalse(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	injectWatchEvent(fakeClient, mcvWithProcessingFalse("ResourceNotFound"))

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	_, err := fetcher.watchMCVResult(context.Background(), "test-cluster", "test-mcv")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func TestWatchMCVResult_ContextCancel(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	// Register a watcher that never sends anything.
	fw := watch.NewFake()
	fakeClient.PrependWatchReactor("managedclusterviews",
		func(_ k8stesting.Action) (bool, watch.Interface, error) {
			return true, fw, nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	_, err := fetcher.watchMCVResult(ctx, "test-cluster", "test-mcv")
	assert.Error(t, err)
}

func TestWatchMCVResult_ChannelClosed(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	fw := watch.NewFake()
	fakeClient.PrependWatchReactor("managedclusterviews",
		func(_ k8stesting.Action) (bool, watch.Interface, error) {
			return true, fw, nil
		})
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Stop() // closes the channel
	}()

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	_, err := fetcher.watchMCVResult(context.Background(), "test-cluster", "test-mcv")
	assert.Error(t, err)
}

// ── fetchPVCStorageClass ──────────────────────────────────────────────────────

func TestFetchPVCStorageClass(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	injectWatchEvent(fakeClient, mcvWithResult(map[string]interface{}{
		keySpec: map[string]interface{}{"storageClassName": "fast-ceph"},
	}))

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	sc, err := fetcher.fetchPVCStorageClass(context.Background(), "cluster-a", "default", "my-pvc")
	require.NoError(t, err)
	assert.Equal(t, "fast-ceph", sc)
}

// ── resolveStorageClasses ─────────────────────────────────────────────────────

func TestResolveStorageClasses_EmptyEntries(t *testing.T) {
	fetcher := &VMFetcher{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	result := fetcher.resolveStorageClasses(context.Background(), api.MigrationTargetRequest{
		ClusterName: "c1", VMNamespace: "default", VMName: "vm1",
	}, nil)
	assert.Nil(t, result)
}

func TestResolveStorageClasses_WithPVC(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

	// The first watch call (for the PVC MCV) returns the storage class.
	fw := watch.NewFake()
	fakeClient.PrependWatchReactor("managedclusterviews",
		func(_ k8stesting.Action) (bool, watch.Interface, error) {
			return true, fw, nil
		})
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(mcvWithResult(map[string]interface{}{
			keySpec: map[string]interface{}{"storageClassName": "ceph-rbd"},
		}))
	}()

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	entries := []volumeEntry{
		{name: "vol1", claimName: "pvc1", sizeBytes: 10 * (1 << 30)},
		{name: "vol-no-claim", claimName: "", sizeBytes: 5 * (1 << 30)}, // no claim → skip
	}
	req := api.MigrationTargetRequest{ClusterName: "c1", VMNamespace: "default", VMName: "vm"}
	vols := fetcher.resolveStorageClasses(context.Background(), req, entries)
	require.Len(t, vols, 2)
	assert.Equal(t, "ceph-rbd", vols[0].StorageClass)
	assert.Equal(t, int64(10*(1<<30)), vols[0].SizeBytes)
	assert.Equal(t, "", vols[1].StorageClass) // no claim → empty SC
}

func TestExtractResourceRequests(t *testing.T) {
	tests := []struct {
		name     string
		object   map[string]interface{}
		wantCPU  int64
		wantMemB int64
		wantErr  bool
	}{
		{
			name: "spec.domain.cpu.cores integer path",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"cpu":    map[string]interface{}{"cores": int64(4)},
						"memory": map[string]interface{}{"guest": "8Gi"},
					},
				},
			},
			wantCPU:  4,
			wantMemB: 8 * (1 << 30),
		},
		{
			name: "millicore CPU is ceil-rounded up to 1",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    "500m",
								"memory": "2Gi",
							},
						},
					},
				},
			},
			wantCPU:  1,
			wantMemB: 2 * (1 << 30),
		},
		{
			name: "fractional CPU string is ceil-rounded",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    "1.5",
								"memory": "4Gi",
							},
						},
					},
				},
			},
			wantCPU:  2,
			wantMemB: 4 * (1 << 30),
		},
		{
			name: "exact integer CPU string",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    "2",
								"memory": "1Gi",
							},
						},
					},
				},
			},
			wantCPU:  2,
			wantMemB: 1 << 30,
		},
		{
			name: "memory fallback to resources.requests when guest missing",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"cpu": map[string]interface{}{"cores": int64(2)},
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"memory": "512Mi",
							},
						},
					},
				},
			},
			wantCPU:  2,
			wantMemB: 512 * (1 << 20),
		},
		{
			name: "CPU parse error does not overwrite with memory error",
			object: map[string]interface{}{
				"spec": map[string]interface{}{
					"domain": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{
								"cpu":    "bad-cpu",
								"memory": "4Gi",
							},
						},
					},
				},
			},
			wantCPU:  0,
			wantMemB: 0, // memory skipped because err != nil from CPU
			wantErr:  true,
		},
		{
			name:     "empty VMI returns zeros without error",
			object:   map[string]interface{}{},
			wantCPU:  0,
			wantMemB: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmi := vmiWithCPUAndMem(t, tt.object)
			cpu, mem, err := extractResourceRequests(vmi)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantCPU, cpu, "cpuCores")
			assert.Equal(t, tt.wantMemB, mem, "memBytes")
		})
	}
}

// ── VMNotFoundError ───────────────────────────────────────────────────────────

// TestVMNotFoundError_Error verifies the error string is returned unchanged.
func TestVMNotFoundError_Error(t *testing.T) {
	e := &VMNotFoundError{msg: "cluster not found on hub"}
	assert.Equal(t, "cluster not found on hub", e.Error())
}

// ── fetchVMIViaMCV ────────────────────────────────────────────────────────────

// TestFetchVMIViaMCV_ClusterNotFound verifies that a NotFound on MCV Create is
// wrapped as a VMNotFoundError (so ServeHTTP returns 400 instead of 500).
func TestFetchVMIViaMCV_ClusterNotFound(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("create", "managedclusterviews",
		func(_ k8stesting.Action) (handled bool, obj runtime.Object, err error) {
			return true, nil, apierrors.NewNotFound(
				schema.GroupResource{
					Group:    managedClusterViewGVR.Group,
					Resource: managedClusterViewGVR.Resource,
				},
				"nonexistent-cluster",
			)
		})

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	_, err := fetcher.fetchVMIViaMCV(context.Background(), api.MigrationTargetRequest{
		ClusterName: "nonexistent-cluster", VMNamespace: "default", VMName: "vm1",
	})
	require.Error(t, err)
	var notFound *VMNotFoundError
	assert.True(t, errors.As(err, &notFound), "expected VMNotFoundError, got %T: %v", err, err)
}

// TestFetchVMIViaMCV_CreateError verifies that a generic Create error (not
// NotFound) is propagated as a plain error, not a VMNotFoundError.
func TestFetchVMIViaMCV_CreateError(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("create", "managedclusterviews",
		func(_ k8stesting.Action) (handled bool, obj runtime.Object, err error) {
			return true, nil, fmt.Errorf("hub API server unavailable")
		})

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	_, err := fetcher.fetchVMIViaMCV(context.Background(), api.MigrationTargetRequest{
		ClusterName: "c1", VMNamespace: "default", VMName: "vm1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create ManagedClusterView")
	var notFound *VMNotFoundError
	assert.False(t, errors.As(err, &notFound), "should NOT be VMNotFoundError")
}

// ── FetchVMInfo ───────────────────────────────────────────────────────────────

// TestFetchVMInfo_Success verifies that a successful MCV watch round-trip
// produces a valid SourceVMInfo with the correct name, namespace, and cluster.
func TestFetchVMInfo_Success(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	injectWatchEvent(fakeClient, mcvWithResult(map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"metadata":   map[string]interface{}{"name": "vm1", "namespace": "default"},
	}))

	fetcher := &VMFetcher{DynamicClient: fakeClient}
	info, err := fetcher.FetchVMInfo(context.Background(), api.MigrationTargetRequest{
		ClusterName: "hub-cluster", VMNamespace: "default", VMName: "vm1",
	})
	require.NoError(t, err)
	assert.Equal(t, "vm1", info.Name)
	assert.Equal(t, "default", info.Namespace)
	assert.Equal(t, "hub-cluster", info.Cluster)
}
