// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package plan

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testClient is a minimal in-memory client.Client.
// Only Get and Patch are implemented; other methods panic if called.
// This avoids importing sigs.k8s.io/controller-runtime/pkg/client/fake,
// which causes linker cache corruption in the Prow build environment.
type testClient struct {
	client.Client // embedded nil; only Get and Patch are overridden
	objects       map[string]*unstructured.Unstructured
	getErr        error // when non-nil, returned by Get for any object
	patchErr      error // when non-nil, returned by Patch for any object
}

func newTestClient(objs ...*unstructured.Unstructured) *testClient {
	c := &testClient{objects: make(map[string]*unstructured.Unstructured)}
	for _, o := range objs {
		c.set(o)
	}
	return c
}

func objMapKey(ns, name, kind string) string { return ns + "/" + name + "/" + kind }

func (c *testClient) set(obj *unstructured.Unstructured) {
	c.objects[objMapKey(obj.GetNamespace(), obj.GetName(), obj.GetKind())] = obj.DeepCopy()
}

func (c *testClient) Get(_ context.Context, nn types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
	if c.getErr != nil {
		return c.getErr
	}
	u := obj.(*unstructured.Unstructured)
	stored, ok := c.objects[objMapKey(nn.Namespace, nn.Name, u.GetKind())]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: u.GetKind()}, nn.Name)
	}
	u.Object = stored.DeepCopy().Object
	return nil
}

func (c *testClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	if c.patchErr != nil {
		return c.patchErr
	}
	c.set(obj.(*unstructured.Unstructured))
	return nil
}

const (
	testPlanName = "test-plan"
	testNetName  = "test-net"
	testStgName  = "test-stg"
	testNS       = "test-ns"
)

func makePlan(netName, netNS, stgName, stgNS string) *unstructured.Unstructured {
	u := newUnstructured(planGVK)
	u.SetName(testPlanName)
	u.SetNamespace(testNS)
	u.SetLabels(map[string]string{labelCreatedBy: labelCCLMValue})
	u.SetUID(types.UID(testPlanName + "-uid"))
	_ = unstructured.SetNestedField(u.Object, netName, "spec", "map", "network", "name")
	_ = unstructured.SetNestedField(u.Object, netNS, "spec", "map", "network", "namespace")
	_ = unstructured.SetNestedField(u.Object, stgName, "spec", "map", "storage", "name")
	_ = unstructured.SetNestedField(u.Object, stgNS, "spec", "map", "storage", "namespace")
	return u
}

func makeNetworkMap(ns string, labeled bool) *unstructured.Unstructured {
	u := newUnstructured(networkMapGVK)
	u.SetName(testNetName)
	u.SetNamespace(ns)
	if labeled {
		u.SetLabels(map[string]string{labelCreatedBy: labelCCLMValue})
	}
	return u
}

func makeStorageMap(ns string, labeled bool) *unstructured.Unstructured {
	u := newUnstructured(storageMapGVK)
	u.SetName(testStgName)
	u.SetNamespace(ns)
	if labeled {
		u.SetLabels(map[string]string{labelCreatedBy: labelCCLMValue})
	}
	return u
}

func planReq() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: testPlanName, Namespace: testNS}}
}

func reconcileWith(t *testing.T, objs ...*unstructured.Unstructured) (ctrl.Result, *testClient) {
	t.Helper()
	c := newTestClient(objs...)
	r := &PlanReconciler{Client: c, Scheme: runtime.NewScheme()}
	result, err := r.Reconcile(context.Background(), planReq())
	require.NoError(t, err)
	return result, c
}

// hasOwnerRef returns true if the stored object (ns/name/kind) has a Plan owner reference.
func hasOwnerRef(c *testClient, kind, ns, name string) bool {
	obj, ok := c.objects[objMapKey(ns, name, kind)]
	if !ok {
		return false
	}
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == planKind && ref.Name == testPlanName {
			return true
		}
	}
	return false
}

// TestPlanReconcile_PlanNotFound verifies that a missing Plan returns no error.
func TestPlanReconcile_PlanNotFound(t *testing.T) {
	result, _ := reconcileWith(t) // no objects at all
	assert.Equal(t, ctrl.Result{}, result)
}

// TestPlanReconcile_SetsOwnerRefOnBothLabeledMaps verifies that OwnerReferences
// are added to both NetworkMap and StorageMap when they carry the cclm label.
func TestPlanReconcile_SetsOwnerRefOnBothLabeledMaps(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)
	sm := makeStorageMap(testNS, true)

	_, c := reconcileWith(t, p, nm, sm)

	assert.True(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName), "NetworkMap should have OwnerReference")
	assert.True(t, hasOwnerRef(c, "StorageMap", testNS, testStgName), "StorageMap should have OwnerReference")
}

// TestPlanReconcile_SkipsNetworkMapWithoutLabel verifies that a NetworkMap
// without the cclm label does not get an OwnerReference.
func TestPlanReconcile_SkipsNetworkMapWithoutLabel(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, false)
	sm := makeStorageMap(testNS, true)

	_, c := reconcileWith(t, p, nm, sm)

	assert.False(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName),
		"NetworkMap without label should NOT get OwnerReference")
	assert.True(t, hasOwnerRef(c, "StorageMap", testNS, testStgName), "StorageMap should still get OwnerReference")
}

// TestPlanReconcile_SkipsStorageMapWithoutLabel verifies that a StorageMap
// without the cclm label does not get an OwnerReference.
func TestPlanReconcile_SkipsStorageMapWithoutLabel(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)
	sm := makeStorageMap(testNS, false)

	_, c := reconcileWith(t, p, nm, sm)

	assert.True(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName), "NetworkMap should get OwnerReference")
	assert.False(t, hasOwnerRef(c, "StorageMap", testNS, testStgName),
		"StorageMap without label should NOT get OwnerReference")
}

// TestPlanReconcile_NetworkMapNotFound verifies that a missing NetworkMap is
// skipped without returning an error.
func TestPlanReconcile_NetworkMapNotFound(t *testing.T) {
	p := makePlan("missing-net", testNS, testStgName, testNS)
	sm := makeStorageMap(testNS, true)

	_, c := reconcileWith(t, p, sm)

	assert.True(t, hasOwnerRef(c, "StorageMap", testNS, testStgName), "StorageMap should still get OwnerReference")
}

// TestPlanReconcile_StorageMapNotFound verifies that a missing StorageMap is
// skipped without returning an error.
func TestPlanReconcile_StorageMapNotFound(t *testing.T) {
	p := makePlan(testNetName, testNS, "missing-stg", testNS)
	nm := makeNetworkMap(testNS, true)

	_, c := reconcileWith(t, p, nm)

	assert.True(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName), "NetworkMap should still get OwnerReference")
}

// TestPlanReconcile_SkipsCrossNamespaceMaps verifies that maps in a different
// namespace than the Plan are skipped without error.
func TestPlanReconcile_SkipsCrossNamespaceMaps(t *testing.T) {
	otherNS := "other-ns"
	p := makePlan(testNetName, otherNS, testStgName, otherNS)
	nm := makeNetworkMap(otherNS, true)
	sm := makeStorageMap(otherNS, true)

	result, c := reconcileWith(t, p, nm, sm)
	assert.Equal(t, ctrl.Result{}, result)

	assert.False(t, hasOwnerRef(c, "NetworkMap", otherNS, testNetName),
		"cross-namespace NetworkMap should NOT get OwnerReference")
	assert.False(t, hasOwnerRef(c, "StorageMap", otherNS, testStgName),
		"cross-namespace StorageMap should NOT get OwnerReference")
}

// TestPlanReconcile_UsesPlanNamespaceWhenMapNamespaceEmpty verifies that a map
// reference with an empty namespace defaults to the Plan's namespace.
func TestPlanReconcile_UsesPlanNamespaceWhenMapNamespaceEmpty(t *testing.T) {
	p := makePlan(testNetName, "", testStgName, "")
	nm := makeNetworkMap(testNS, true)
	sm := makeStorageMap(testNS, true)

	_, c := reconcileWith(t, p, nm, sm)

	assert.True(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName))
	assert.True(t, hasOwnerRef(c, "StorageMap", testNS, testStgName))
}

// TestPlanReconcile_ReplacesStaleOwnerRef verifies that a NetworkMap carrying
// a controller OwnerReference for the same Plan name but a different (stale) UID
// gets its OwnerReference updated to the current Plan's UID.
func TestPlanReconcile_ReplacesStaleOwnerRef(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)

	// Pre-stamp a stale Plan owner ref with the correct name but a different UID.
	staleUID := types.UID(testPlanName + "-old-uid")
	isController := true
	blockOwnerDeletion := true
	nm.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         planGVK.Group + "/" + planGVK.Version,
		Kind:               planGVK.Kind,
		Name:               testPlanName,
		UID:                staleUID,
		Controller:         &isController,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}})

	sm := makeStorageMap(testNS, true)
	_, c := reconcileWith(t, p, nm, sm)

	// The stored NetworkMap must have exactly one Plan owner ref with the NEW UID.
	stored := c.objects[objMapKey(testNS, testNetName, "NetworkMap")]
	require.NotNil(t, stored)
	var planRefs []metav1.OwnerReference
	for _, ref := range stored.GetOwnerReferences() {
		if ref.Kind == planKind && ref.Name == testPlanName {
			planRefs = append(planRefs, ref)
		}
	}
	require.Len(t, planRefs, 1, "should have exactly one Plan owner ref after stale replacement")
	assert.Equal(t, p.GetUID(), planRefs[0].UID, "owner ref UID should be the current Plan's UID, not the stale one")
}

// TestPlanReconcile_GetErrorPropagates verifies that a non-NotFound error
// returned by Get for a map resource propagates out of Reconcile with a
// descriptive wrapper message.
func TestPlanReconcile_GetErrorPropagates(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)

	c := newTestClient(p, nm)
	// Make the client return a generic server error for any Get after the Plan
	// itself has been fetched. We do this by pre-loading the Plan but injecting
	// the error only for the map lookups. The simplest way is to remove nm from
	// the store and set a getErr that fires for all objects including Plan;
	// instead we use a thin wrapper that skips the Plan kind.
	errClient := &mapGetErrClient{testClient: c, mapKind: "NetworkMap"}
	r := &PlanReconciler{Client: errClient, Scheme: runtime.NewScheme()}
	_, err := r.Reconcile(context.Background(), planReq())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NetworkMap")
}

// mapGetErrClient wraps testClient and injects a generic error when Get is
// called for a specific resource Kind (simulating an API server error that is
// not a "not found").
type mapGetErrClient struct {
	*testClient
	mapKind string
}

func (c *mapGetErrClient) Get(
	ctx context.Context, nn types.NamespacedName, obj client.Object, opts ...client.GetOption,
) error {
	u := obj.(*unstructured.Unstructured)
	if u.GetKind() == c.mapKind {
		return fmt.Errorf("internal server error")
	}
	return c.testClient.Get(ctx, nn, obj, opts...)
}

func (c *mapGetErrClient) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption,
) error {
	return c.testClient.Patch(ctx, obj, patch, opts...)
}

// TestPlanReconcile_SkipsMapOwnedByDifferentController verifies that a map
// already controlled by a genuinely different controller (not a Plan) is left
// untouched.
func TestPlanReconcile_SkipsMapOwnedByDifferentController(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)

	otherController := true
	nm.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "other-controller",
		UID:        types.UID("other-uid"),
		Controller: &otherController,
	}})

	sm := makeStorageMap(testNS, true)
	_, c := reconcileWith(t, p, nm, sm)

	// NetworkMap should NOT have a Plan OwnerReference since another controller owns it.
	assert.False(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName),
		"NetworkMap owned by a different controller should not get a Plan OwnerReference")
	// StorageMap has no prior owner and should be claimed.
	assert.True(t, hasOwnerRef(c, "StorageMap", testNS, testStgName))
}

// TestPlanReconcile_PreservesNonControllerOwnerRefs verifies that existing
// non-controller OwnerReferences on a map are preserved when the Plan owner
// reference is added.
func TestPlanReconcile_PreservesNonControllerOwnerRefs(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)

	// A non-controller ref (no Controller field set) that should survive.
	nm.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Name:       "some-config",
		UID:        types.UID("cfg-uid"),
	}})

	_, c := reconcileWith(t, p, nm)

	stored := c.objects[objMapKey(testNS, testNetName, "NetworkMap")]
	require.NotNil(t, stored)

	var found bool
	for _, ref := range stored.GetOwnerReferences() {
		if ref.Kind == "ConfigMap" && ref.Name == "some-config" {
			found = true
		}
	}
	assert.True(t, found, "non-controller OwnerReference should be preserved")
	assert.True(t, hasOwnerRef(c, "NetworkMap", testNS, testNetName),
		"Plan OwnerReference should also be added")
}

// TestPlanReconcile_PatchErrorPropagates verifies that a Patch failure from
// setOwner is returned with a descriptive message.
func TestPlanReconcile_PatchErrorPropagates(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)
	sm := makeStorageMap(testNS, true)

	c := newTestClient(p, nm, sm)
	c.patchErr = fmt.Errorf("patch refused by webhook")
	r := &PlanReconciler{Client: c, Scheme: runtime.NewScheme()}

	_, err := r.Reconcile(context.Background(), planReq())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "patch owner reference")
}

// TestPlanReconcile_Idempotent verifies that reconciling twice does not error
// and the OwnerReference remains set exactly once.
func TestPlanReconcile_Idempotent(t *testing.T) {
	p := makePlan(testNetName, testNS, testStgName, testNS)
	nm := makeNetworkMap(testNS, true)
	sm := makeStorageMap(testNS, true)

	c := newTestClient(p, nm, sm)
	r := &PlanReconciler{Client: c, Scheme: runtime.NewScheme()}

	_, err := r.Reconcile(context.Background(), planReq())
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), planReq())
	require.NoError(t, err, "second reconcile should not error")

	nmStored := c.objects[objMapKey(testNS, testNetName, "NetworkMap")]
	require.NotNil(t, nmStored)

	count := 0
	for _, ref := range nmStored.GetOwnerReferences() {
		if ref.Kind == planKind && ref.Name == testPlanName {
			count++
		}
	}
	assert.Equal(t, 1, count, "OwnerReference should appear exactly once after two reconciles")
}
