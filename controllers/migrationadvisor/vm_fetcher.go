package migrationadvisor

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
)

// VMNotFoundError is returned by VMFetcher when the requested cluster does not
// exist as a hub namespace, or when the VMI/PVC cannot be found on the spoke.
// ServeHTTP maps this to HTTP 400 Bad Request so callers get a meaningful error
// instead of a generic 500.
type VMNotFoundError struct {
	msg string
}

func (e *VMNotFoundError) Error() string { return e.msg }

var (
	managedClusterViewGVR = schema.GroupVersionResource{
		Group:    "view.open-cluster-management.io",
		Version:  "v1beta1",
		Resource: "managedclusterviews",
	}
)

const mcvWatchTimeout = 30 * time.Second

const (
	managedClusterViewAPIVersion = "view.open-cluster-management.io/v1beta1"
	managedClusterViewKind       = "ManagedClusterView"

	keyAPIVersion = "apiVersion"
	keyKind       = "kind"
	keyMetadata   = "metadata"
	keyName       = "name"
	keyNamespace  = "namespace"
	keySpec       = "spec"
)

// VMFetcher fetches VM resource and storage info entirely via ManagedClusterView:
//
//  1. Fetch the VirtualMachineInstance by name → CPU, memory, PVC claimNames + sizes.
//  2. For each PVC claimName, fetch the PVC in parallel → spec.storageClassName.
//
// No cluster proxy is required — all access is hub-side via ManagedClusterView CRs.
type VMFetcher struct {
	// DynamicClient is a hub dynamic client used to create/watch/delete ManagedClusterViews.
	DynamicClient dynamic.Interface
}

// FetchVMInfo retrieves CPU/memory requests and volume info (including StorageClass names).
func (f *VMFetcher) FetchVMInfo(ctx context.Context, req api.MigrationTargetRequest) (*api.SourceVMInfo, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("vm", req.VMName, "namespace", req.VMNamespace, "cluster", req.ClusterName)

	// Step 1: ManagedClusterView — fetch the VMI by name.
	// Gives CPU, memory, and { claimName, size } per PVC-backed volume.
	// (ManagedClusterView requires a resource name; it cannot list resources.)
	vmi, err := f.fetchVMIViaMCV(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch VMI: %w", err)
	}

	cpuCores, memBytes, err := extractResourceRequests(vmi)
	if err != nil {
		log.Error(err, "could not extract resource requests from VMI spec, defaulting to 0")
	}

	volumeEntries := extractVolumeEntriesFromVMI(vmi)
	log.Info("extracted PVC-backed volumes from VMI", "count", len(volumeEntries))

	// Step 2: ManagedClusterView — fetch PVCs to resolve storageClassName per volume.
	volumes := f.resolveStorageClasses(ctx, req, volumeEntries)

	return &api.SourceVMInfo{
		Name:        req.VMName,
		Namespace:   req.VMNamespace,
		Cluster:     req.ClusterName,
		CPUCores:    cpuCores,
		MemoryBytes: memBytes,
		Volumes:     volumes,
	}, nil
}

// fetchVMIViaMCV creates a temporary ManagedClusterView (name is required) and waits
// for the single VirtualMachineInstance result, then deletes the view.
func (f *VMFetcher) fetchVMIViaMCV(
	ctx context.Context,
	req api.MigrationTargetRequest,
) (*unstructured.Unstructured, error) {
	mcvName := fmt.Sprintf("migration-advisor-vmi-%s", req.VMName)

	mcv := &unstructured.Unstructured{
		Object: map[string]interface{}{
			keyAPIVersion: managedClusterViewAPIVersion,
			keyKind:       managedClusterViewKind,
			keyMetadata: map[string]interface{}{
				keyName:      mcvName,
				keyNamespace: req.ClusterName,
			},
			keySpec: map[string]interface{}{
				"scope": map[string]interface{}{
					"apiGroup":   "kubevirt.io",
					"version":    "v1",
					"resource":   "virtualmachineinstances",
					keyName:      req.VMName, // required — ManagedClusterView cannot list
					keyNamespace: req.VMNamespace,
				},
			},
		},
	}

	_, err := f.DynamicClient.Resource(managedClusterViewGVR).
		Namespace(req.ClusterName).
		Create(ctx, mcv, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		if apierrors.IsNotFound(err) {
			return nil, &VMNotFoundError{
				msg: fmt.Sprintf("cluster %q not found on hub (no namespace %q)", req.ClusterName, req.ClusterName),
			}
		}
		return nil, fmt.Errorf("create ManagedClusterView: %w", err)
	}

	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.DynamicClient.Resource(managedClusterViewGVR).
			Namespace(req.ClusterName).
			Delete(dctx, mcvName, metav1.DeleteOptions{})
	}()

	return f.watchMCVResult(ctx, req.ClusterName, mcvName)
}

// watchMCVResult watches the ManagedClusterView and returns its result the instant
// the spoke agent sets Processing=False. Using Watch instead of polling eliminates
// the 2-second poll interval — the result is received within milliseconds of the
// spoke reporting back, with a single long-lived connection rather than repeated GETs.
func (f *VMFetcher) watchMCVResult(
	ctx context.Context,
	clusterNamespace, mcvName string,
) (*unstructured.Unstructured, error) {
	timeoutSecs := int64(mcvWatchTimeout.Seconds())

	watcher, err := f.DynamicClient.Resource(managedClusterViewGVR).
		Namespace(clusterNamespace).
		Watch(ctx, metav1.ListOptions{
			FieldSelector:  fmt.Sprintf("metadata.name=%s", mcvName),
			TimeoutSeconds: &timeoutSecs,
		})
	if err != nil {
		return nil, fmt.Errorf("watch ManagedClusterView: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case event, ok := <-watcher.ResultChan():
			if !ok {
				return nil, fmt.Errorf("timed out waiting for ManagedClusterView %s/%s", clusterNamespace, mcvName)
			}

			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			// Return as soon as status.result is populated.
			// Some MCV implementations stay in Processing=True ("Watching" mode)
			// indefinitely and never transition to Processing=False, but still
			// populate status.result immediately.  Checking for a non-nil result
			// first handles both the classic (Processing=False) and continuous-watch
			// (Processing=True) MCV behaviors.
			if result, found, _ := unstructured.NestedMap(
				obj.Object, "status", "result",
			); found && len(result) > 0 {
				return &unstructured.Unstructured{Object: result}, nil
			}

			// Fallback: also check for Processing=False (error case — no result).
			conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, c := range conditions {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				condType, _, _ := unstructured.NestedString(cond, "type")
				condStatus, _, _ := unstructured.NestedString(cond, "status")
				condReason, _, _ := unstructured.NestedString(cond, "reason")
				if condType == "Processing" && condStatus == "False" {
					// GetResourceFailed means the spoke could not find the requested resource.
					// Treat it as a user-visible "not found" rather than a server error.
					if condReason == "GetResourceFailed" {
						return nil, &VMNotFoundError{
							msg: fmt.Sprintf("ManagedClusterView %s/%s: resource not found on spoke", clusterNamespace, mcvName),
						}
					}
					return nil, fmt.Errorf("ManagedClusterView %s/%s failed: %s", clusterNamespace, mcvName, condReason)
				}
			}
		}
	}
}

// extractResourceRequests reads CPU cores and memory from VMI spec.
//
// CPU priority:    spec.domain.cpu.cores  →  spec.domain.resources.requests.cpu
// Memory priority: spec.domain.memory.guest  →  spec.domain.resources.requests.memory
func extractResourceRequests(vmi *unstructured.Unstructured) (cpuCores int64, memBytes int64, err error) {
	if cores, found, _ := unstructured.NestedInt64(vmi.Object, keySpec, "domain", "cpu", "cores"); found && cores > 0 {
		cpuCores = cores
	} else {
		cpuStr, found, _ := unstructured.NestedString(
			vmi.Object, keySpec, "domain", "resources", "requests", "cpu",
		)
		if found && cpuStr != "" {
			f, parseErr := parseCPUCores(cpuStr)
			if parseErr != nil {
				err = parseErr
			} else {
				if f > 0 {
					cpuCores = int64(math.Ceil(f))
				}
			}
		}
	}

	if err == nil {
		memStr, found, _ := unstructured.NestedString(
			vmi.Object, keySpec, "domain", "memory", "guest",
		)
		if found && memStr != "" {
			memBytes, err = parseQuantityToBytes(memStr)
		} else {
			memStr, found, _ = unstructured.NestedString(
				vmi.Object, keySpec, "domain", "resources", "requests", "memory",
			)
			if found && memStr != "" {
				memBytes, err = parseQuantityToBytes(memStr)
			}
		}
	}

	return cpuCores, memBytes, err
}

// volumeEntry holds intermediate data extracted from VMI status before the SC is resolved.
type volumeEntry struct {
	name      string
	claimName string // PVC name on the spoke cluster (== DataVolume name for CDI volumes)
	sizeBytes int64
}

// extractVolumeEntriesFromVMI reads status.volumeStatus and returns one entry per
// PVC-backed volume. Volumes without persistentVolumeClaimInfo are skipped
// (e.g. cloudinitdisk, containerDisk).
//
// Note: persistentVolumeClaimInfo does NOT contain storageClassName — only claimName.
func extractVolumeEntriesFromVMI(vmi *unstructured.Unstructured) []volumeEntry {
	rawStatuses, _, _ := unstructured.NestedSlice(vmi.Object, "status", "volumeStatus")

	entries := make([]volumeEntry, 0, len(rawStatuses))
	for _, raw := range rawStatuses {
		vs, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		pvcInfo, found, _ := unstructured.NestedMap(vs, "persistentVolumeClaimInfo")
		if !found {
			continue
		}

		name, _, _ := unstructured.NestedString(vs, "name")
		claimName, _, _ := unstructured.NestedString(pvcInfo, "claimName")

		sizeStr, _, _ := unstructured.NestedString(pvcInfo, "capacity", "storage")
		if sizeStr == "" {
			sizeStr, _, _ = unstructured.NestedString(pvcInfo, "requests", "storage")
		}
		sizeBytes, parseErr := parseQuantityToBytes(sizeStr)
		if parseErr != nil {
			ctrl.Log.Error(parseErr, "failed to parse PVC storage capacity; volume size will be treated as 0, "+
				"which may cause the Ceph capacity check to pass incorrectly",
				"claimName", claimName, "rawSize", sizeStr)
		}

		entries = append(entries, volumeEntry{
			name:      name,
			claimName: claimName,
			sizeBytes: sizeBytes,
		})
	}
	return entries
}

// resolveStorageClasses determines the StorageClass for each PVC-backed volume by
// creating a temporary ManagedClusterView for each PVC in parallel.
//
// This requires no cluster proxy — it uses the same hub-side MCV machinery as
// fetchVMIViaMCV. StorageClass errors are non-fatal: the volume is included with
// an empty StorageClass, which will cause the scorer to skip clusters that lack
// a matching SC.
func (f *VMFetcher) resolveStorageClasses(
	ctx context.Context,
	req api.MigrationTargetRequest,
	entries []volumeEntry,
) []api.VMVolumeInfo {
	if len(entries) == 0 {
		return nil
	}

	log := ctrl.LoggerFrom(ctx)

	type pvcResult struct {
		sc  string
		err error
	}
	pvcResults := make([]pvcResult, len(entries))

	var wg sync.WaitGroup
	for i, e := range entries {
		if e.claimName == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, entry volumeEntry) {
			defer wg.Done()
			sc, err := f.fetchPVCStorageClass(ctx, req.ClusterName, req.VMNamespace, entry.claimName)
			pvcResults[idx] = pvcResult{sc: sc, err: err}
		}(i, e)
	}
	wg.Wait()

	volumes := make([]api.VMVolumeInfo, len(entries))
	for i, e := range entries {
		if pvcResults[i].err != nil {
			log.Error(pvcResults[i].err, "could not resolve StorageClass for PVC, leaving empty",
				"volume", e.name, "pvc", e.claimName)
		}
		volumes[i] = api.VMVolumeInfo{
			Name:         e.name,
			StorageClass: pvcResults[i].sc,
			SizeBytes:    e.sizeBytes,
		}
	}
	return volumes
}

// fetchPVCStorageClass creates a temporary ManagedClusterView to read a single PVC
// on the spoke cluster and returns its spec.storageClassName.
func (f *VMFetcher) fetchPVCStorageClass(
	ctx context.Context,
	clusterName, namespace, claimName string,
) (string, error) {
	const prefix = "migration-advisor-pvc-"
	suffix := claimName
	if len(prefix)+len(suffix) > 253 {
		suffix = suffix[:253-len(prefix)]
	}
	mcvName := prefix + suffix

	mcv := &unstructured.Unstructured{
		Object: map[string]interface{}{
			keyAPIVersion: managedClusterViewAPIVersion,
			keyKind:       managedClusterViewKind,
			keyMetadata: map[string]interface{}{
				keyName:      mcvName,
				keyNamespace: clusterName,
			},
			keySpec: map[string]interface{}{
				"scope": map[string]interface{}{
					"apiGroup":   "",
					"version":    "v1",
					"resource":   "persistentvolumeclaims",
					keyName:      claimName,
					keyNamespace: namespace,
				},
			},
		},
	}

	_, err := f.DynamicClient.Resource(managedClusterViewGVR).
		Namespace(clusterName).
		Create(ctx, mcv, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create ManagedClusterView for PVC %s: %w", claimName, err)
	}

	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.DynamicClient.Resource(managedClusterViewGVR).
			Namespace(clusterName).
			Delete(dctx, mcvName, metav1.DeleteOptions{})
	}()

	pvc, err := f.watchMCVResult(ctx, clusterName, mcvName)
	if err != nil {
		return "", fmt.Errorf("watch MCV for PVC %s: %w", claimName, err)
	}

	sc, _, _ := unstructured.NestedString(pvc.Object, keySpec, "storageClassName")
	return sc, nil
}
