package webhook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestBindingNamespacesCoverTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		binding  map[string]interface{}
		targetNS string
		want     bool
	}{
		{
			name: "star covers any namespace",
			binding: map[string]interface{}{
				"namespaces": []interface{}{"*"},
			},
			targetNS: "openshift-mtv",
			want:     true,
		},
		{
			name: "explicit namespace match",
			binding: map[string]interface{}{
				"namespaces": []interface{}{"openshift-mtv", "default"},
			},
			targetNS: "openshift-mtv",
			want:     true,
		},
		{
			name: "no match when namespace missing",
			binding: map[string]interface{}{
				"namespaces": []interface{}{"other-ns"},
			},
			targetNS: "openshift-mtv",
			want:     false,
		},
		{
			name:     "missing namespaces field",
			binding:  map[string]interface{}{"cluster": "c1"},
			targetNS: "openshift-mtv",
			want:     false,
		},
		{
			name: "empty namespaces list",
			binding: map[string]interface{}{
				"namespaces": []interface{}{},
			},
			targetNS: "openshift-mtv",
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bindingNamespacesCoverTarget(tc.binding, tc.targetNS)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestUserPermissionCoversTarget(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()

	t.Run("not found returns false without error", func(t *testing.T) {
		t.Parallel()
		client := fake.NewSimpleDynamicClient(scheme)
		ok, err := userPermissionCoversTarget(context.Background(), client, userPermissionManagedClusterAdmin, "c1", "ns1")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("binding matches cluster and star namespace", func(t *testing.T) {
		t.Parallel()
		up := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{
				"cluster":    "c1",
				"namespaces": []interface{}{"*"},
				"scope":      "cluster",
			},
		})
		client := fake.NewSimpleDynamicClient(scheme, up)
		ok, err := userPermissionCoversTarget(context.Background(), client, userPermissionManagedClusterAdmin, "c1", "any-ns")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("wrong cluster no match", func(t *testing.T) {
		t.Parallel()
		up := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{
				"cluster":    "other",
				"namespaces": []interface{}{"*"},
			},
		})
		client := fake.NewSimpleDynamicClient(scheme, up)
		ok, err := userPermissionCoversTarget(context.Background(), client, userPermissionManagedClusterAdmin, "c1", "ns1")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("cluster matches but namespace not allowed", func(t *testing.T) {
		t.Parallel()
		up := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{
				"cluster":    "c1",
				"namespaces": []interface{}{"other-ns"},
			},
		})
		client := fake.NewSimpleDynamicClient(scheme, up)
		ok, err := userPermissionCoversTarget(context.Background(), client, userPermissionManagedClusterAdmin, "c1", "ns1")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("no status bindings", func(t *testing.T) {
		t.Parallel()
		up := userPermissionObject(userPermissionManagedClusterAdmin, nil)
		client := fake.NewSimpleDynamicClient(scheme, up)
		ok, err := userPermissionCoversTarget(context.Background(), client, userPermissionManagedClusterAdmin, "c1", "ns1")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestValidateTargetAccessViaUserPermissions(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()

	t.Run("deny when neither permission grants access", func(t *testing.T) {
		t.Parallel()
		mc := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{"cluster": "other", "namespaces": []interface{}{"*"}},
		})
		kv := userPermissionObject(userPermissionKubevirtAdmin, []map[string]interface{}{
			{"cluster": "other2", "namespaces": []interface{}{"*"}},
		})
		client := fake.NewSimpleDynamicClient(scheme, mc, kv)
		ok, err := validateTargetAccessViaUserPermissions(context.Background(), client, "target", "ns1")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("allow when managedcluster admin matches", func(t *testing.T) {
		t.Parallel()
		mc := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{"cluster": "target", "namespaces": []interface{}{"*"}},
		})
		kv := userPermissionObject(userPermissionKubevirtAdmin, []map[string]interface{}{
			{"cluster": "other", "namespaces": []interface{}{"*"}},
		})
		client := fake.NewSimpleDynamicClient(scheme, mc, kv)
		ok, err := validateTargetAccessViaUserPermissions(context.Background(), client, "target", "ns1")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("allow when only kubevirt admin matches", func(t *testing.T) {
		t.Parallel()
		mc := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{"cluster": "other", "namespaces": []interface{}{"*"}},
		})
		kv := userPermissionObject(userPermissionKubevirtAdmin, []map[string]interface{}{
			{"cluster": "target", "namespaces": []interface{}{"ns1"}},
		})
		client := fake.NewSimpleDynamicClient(scheme, mc, kv)
		ok, err := validateTargetAccessViaUserPermissions(context.Background(), client, "target", "ns1")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("allow when managedcluster missing but kubevirt matches", func(t *testing.T) {
		t.Parallel()
		kv := userPermissionObject(userPermissionKubevirtAdmin, []map[string]interface{}{
			{"cluster": "target", "namespaces": []interface{}{"*"}},
		})
		client := fake.NewSimpleDynamicClient(scheme, kv)
		ok, err := validateTargetAccessViaUserPermissions(context.Background(), client, "target", "ns1")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("allow when kubevirt missing but managedcluster matches", func(t *testing.T) {
		t.Parallel()
		mc := userPermissionObject(userPermissionManagedClusterAdmin, []map[string]interface{}{
			{"cluster": "target", "namespaces": []interface{}{"*"}},
		})
		client := fake.NewSimpleDynamicClient(scheme, mc)
		ok, err := validateTargetAccessViaUserPermissions(context.Background(), client, "target", "ns1")
		require.NoError(t, err)
		assert.True(t, ok)
	})
}

func TestRawToPlan(t *testing.T) {
	t.Parallel()
	t.Run("empty raw returns nil nil", func(t *testing.T) {
		t.Parallel()
		p, err := rawToPlan(runtime.RawExtension{})
		require.NoError(t, err)
		assert.Nil(t, p)
	})

	t.Run("valid plan json", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
			"apiVersion": "forklift.konveyor.io/v1beta1",
			"kind": "Plan",
			"spec": {
				"targetNamespace": "openshift-mtv",
				"provider": {
					"source": {"name": "src"},
					"destination": {"name": "cluster-mtv"}
				},
				"map": {"network": {}, "storage": {}},
				"vms": []
			}
		}`)
		got, err := rawToPlan(runtime.RawExtension{Raw: raw})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "openshift-mtv", got.Spec.TargetNamespace)
		assert.Equal(t, "cluster-mtv", got.Spec.Provider.Destination.Name)
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		t.Parallel()
		_, err := rawToPlan(runtime.RawExtension{Raw: []byte(`{`)})
		require.Error(t, err)
	})
}

// userPermissionObject builds a cluster-scoped UserPermission unstructured for the fake dynamic client.
func userPermissionObject(name string, bindings []map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(userPermissionGVK())
	u.SetName(name)
	if len(bindings) > 0 {
		sl := make([]interface{}, 0, len(bindings))
		for _, b := range bindings {
			sl = append(sl, b)
		}
		_ = unstructured.SetNestedSlice(u.Object, sl, "status", "bindings")
	}
	return u
}

func userPermissionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   userPermissionGVR.Group,
		Version: userPermissionGVR.Version,
		Kind:    "UserPermission",
	}
}
