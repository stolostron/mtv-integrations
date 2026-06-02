package plan

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	u := obj.(*unstructured.Unstructured)
	stored, ok := c.objects[objMapKey(nn.Namespace, nn.Name, u.GetKind())]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: u.GetKind()}, nn.Name)
	}
	u.Object = stored.DeepCopy().Object
	return nil
}

func (c *testClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
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
