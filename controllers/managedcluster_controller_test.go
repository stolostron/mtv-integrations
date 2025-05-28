package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynFake "k8s.io/client-go/dynamic/fake"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	auth "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestManagedClusterMTVName(t *testing.T) {
	assert.Equal(t, "foo-mtv", managedClusterMTVName("foo"))
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster).Build()
	dynClient := dynFake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	_, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	var updated = &clusterv1.ManagedCluster{}
	_ = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster"}, updated)
	assert.NotContains(t, updated.Finalizers, ManagedClusterFinalizer)

	managedCluster.SetLabels(map[string]string{LabelCNVOperatorInstall: "true"})
	_ = k8sClient.Update(context.TODO(), managedCluster)
	_, err = reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)
	_ = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster"}, updated)
	assert.Contains(t, updated.Finalizers, ManagedClusterFinalizer)
}

func TestReconcile_CreatesManagedServiceAccount(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Labels:     map[string]string{LabelCNVOperatorInstall: "true"},
			Finalizers: []string{ManagedClusterFinalizer},
		},
		Spec: clusterv1.ManagedClusterSpec{
			ManagedClusterClientConfigs: []clusterv1.ClientConfig{
				{URL: "https://example.com"},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster).Build()
	dynClient := dynFake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	_, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	msa := &auth.ManagedServiceAccount{}
	err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster-mtv", Namespace: "test-cluster"}, msa)
	assert.NoError(t, err)
	assert.Equal(t, "test-cluster-mtv", msa.Name)
	assert.Equal(t, "test-cluster", msa.Namespace)
	assert.True(t, msa.Spec.Rotation.Enabled)
	assert.Equal(t, time.Minute*60, msa.Spec.Rotation.Validity.Duration)
}

func TestReconcile_CreatesClusterPermission(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Labels:     map[string]string{LabelCNVOperatorInstall: "true"},
			Finalizers: []string{ManagedClusterFinalizer},
		},
		Spec: clusterv1.ManagedClusterSpec{
			ManagedClusterClientConfigs: []clusterv1.ClientConfig{
				{URL: "https://example.com"},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster).Build()
	dynClient := dynFake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	_, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	msa := &auth.ManagedServiceAccount{}
	err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster-mtv", Namespace: "test-cluster"}, msa)
	assert.NoError(t, err)
	assert.Equal(t, "test-cluster-mtv", msa.Name)
	assert.Equal(t, "test-cluster", msa.Namespace)
	assert.True(t, msa.Spec.Rotation.Enabled)
	assert.Equal(t, time.Minute*60, msa.Spec.Rotation.Validity.Duration)

	// The next reconcile results in the create of the ClusterPermission
	_, err = reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	// Check that the ClusterPermission was created in the fake dynamic client
	u, err := dynClient.Resource(ClusterPermissionsGVR).Namespace("test-cluster").Get(context.TODO(),
		"test-cluster-mtv", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.Equal(t, "test-cluster-mtv", u.GetName())
	assert.Equal(t, "test-cluster", u.GetNamespace())

	// Compare the spec of the ClusterPermission with the expected payload from payloads.go
	expectedSpec := clusterPermissionPayload(managedCluster) // assuming this function exists in payloads.go
	actualSpec, found, err := unstructured.NestedMap(u.Object, "spec")
	assert.NoError(t, err)
	assert.True(t, found, "spec field not found in ClusterPermission")
	assert.Equal(t, expectedSpec["spec"], actualSpec)
}

func TestReconcile_CreatesProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Labels:     map[string]string{LabelCNVOperatorInstall: "true"},
			Finalizers: []string{ManagedClusterFinalizer},
		},
		Spec: clusterv1.ManagedClusterSpec{
			ManagedClusterClientConfigs: []clusterv1.ClientConfig{
				{URL: "https://example.com"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-mtv",
			Namespace: "test-cluster",
		},
		Data: map[string][]byte{
			"token":              []byte("test-token"),
			"ca.crt":             []byte("test-ca"),
			"insecureSkipVerify": []byte("false"),
			"url":                []byte("https://api.example.com:6443"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster, secret).Build()
	dynClient := dynFake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	_, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	msa := &auth.ManagedServiceAccount{}
	err = k8sClient.Get(context.TODO(),
		types.NamespacedName{Name: "test-cluster-mtv", Namespace: "test-cluster"}, msa)
	assert.NoError(t, err)
	assert.Equal(t, "test-cluster-mtv", msa.Name)
	assert.Equal(t, "test-cluster", msa.Namespace)
	assert.True(t, msa.Spec.Rotation.Enabled)
	assert.Equal(t, time.Minute*60, msa.Spec.Rotation.Validity.Duration)

	// The next reconcile results in the create of the ClusterPermission
	assert.NoError(t, err)

	// Set the correct status on the ManagedServiceAccount
	msa = &auth.ManagedServiceAccount{}
	err = k8sClient.Get(context.TODO(),
		types.NamespacedName{Name: "test-cluster-mtv", Namespace: "test-cluster"}, msa)
	assert.NoError(t, err)
	msa.Status.TokenSecretRef = &auth.SecretRef{Name: "test-cluster-mtv"}
	// use Update and not Status().Update as the fake client did not create a sub-resource
	err = k8sClient.Update(context.TODO(), msa)
	assert.NoError(t, err)

	// This reconcile will create the Providera if the secret is present and the ManagedServiceAccount
	// has the correct status tokenSecretRef
	_, err = reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)
	// Check that the Provider was created in the fake dynamic client
	u, err := dynClient.Resource(ProvidersGVR).Namespace("mtv-integrations").Get(context.TODO(),
		"test-cluster-mtv", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.Equal(t, "test-cluster-mtv", u.GetName())
	assert.Equal(t, "mtv-integrations", u.GetNamespace())
	// Check that the Provider secret was created in the fake dynamic client
	err = k8sClient.Get(context.TODO(),
		types.NamespacedName{Name: "test-cluster-mtv", Namespace: "mtv-integrations"}, &corev1.Secret{})
	assert.NoError(t, err)
}
func TestCleanupManagedClusterResources_RemovesFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Finalizers: []string{ManagedClusterFinalizer},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster).Build()
	dynClient := dynFake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	err := reconciler.cleanupManagedClusterResources(context.TODO(), managedCluster)
	assert.NoError(t, err)

	updated := &clusterv1.ManagedCluster{}
	_ = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster"}, updated)
	assert.NotContains(t, updated.Finalizers, ManagedClusterFinalizer)
}
