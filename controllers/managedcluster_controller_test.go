package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	auth "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var providerCrd = &apiextensionsv1.CustomResourceDefinition{
	ObjectMeta: metav1.ObjectMeta{Name: ProviderCRDName},
	Status: apiextensionsv1.CustomResourceDefinitionStatus{
		Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
			{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			},
		},
	},
}

func TestManagedClusterMTVName(t *testing.T) {
	assert.Equal(t, "foo-mtv", managedClusterMTVName("foo"))
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(providerCrd, managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	_, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})
	assert.NoError(t, err)

	updated := &clusterv1.ManagedCluster{}
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
	_ = apiextensionsv1.AddToScheme(scheme)

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

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(providerCrd, managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

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
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

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

	defaultNamespace := "open-cluster-management-agent-addon"

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-serviceaccount-addon-agent",
			Namespace: defaultNamespace,
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(providerCrd, managedCluster, deployment).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

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
	expectedSpec := clusterPermissionPayload(managedCluster,
		defaultNamespace) // assuming this function exists in payloads.go
	actualSpec, found, err := unstructured.NestedMap(u.Object, "spec")
	assert.NoError(t, err)
	assert.True(t, found, "spec field not found in ClusterPermission")
	assert.Equal(t, expectedSpec["spec"], actualSpec)
	// break down nested maps to avoid long lines
	expectedSpecMap := expectedSpec["spec"].(map[string]interface{})
	expectedCRB := expectedSpecMap["clusterRoleBinding"].(map[string]interface{})
	expectedSubject := expectedCRB["subject"].(map[string]interface{})
	assert.Equal(t, expectedSubject["namespace"], defaultNamespace)
}

func TestReconcile_CreatesProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

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
	defaultNamespace := "open-cluster-management-agent-addon"
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-serviceaccount-addon-agent",
			Namespace: defaultNamespace,
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(providerCrd, managedCluster, secret, deployment).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

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
	_ = apiextensionsv1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Finalizers: []string{ManagedClusterFinalizer},
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(providerCrd, managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

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

func TestCleanupManagedClusterResources_DeletesResources(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = auth.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Finalizers: []string{ManagedClusterFinalizer},
		},
	}

	// Seed the dynamic client with resources that should be deleted.
	cp := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.open-cluster-management.io/v1alpha1",
		"kind":       "ClusterPermission",
		"metadata": map[string]interface{}{
			"name":      "test-cluster-mtv",
			"namespace": "test-cluster",
		},
	}}
	msa := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "authentication.open-cluster-management.io/v1beta1",
		"kind":       "ManagedServiceAccount",
		"metadata": map[string]interface{}{
			"name":      "test-cluster-mtv",
			"namespace": "test-cluster",
		},
	}}
	secret := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      "test-cluster-mtv",
			"namespace": MTVIntegrationsNamespace,
		},
	}}
	provider := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "forklift.konveyor.io/v1beta1",
		"kind":       "Provider",
		"metadata": map[string]interface{}{
			"name":      "test-cluster-mtv",
			"namespace": MTVIntegrationsNamespace,
		},
	}}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme, cp, msa, secret, provider)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	err := reconciler.cleanupManagedClusterResources(context.TODO(), managedCluster)
	assert.NoError(t, err)

	// Verify each resource is deleted from the fake dynamic client.
	_, err = dynClient.Resource(ClusterPermissionsGVR).Namespace("test-cluster").Get(
		context.TODO(), "test-cluster-mtv", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected NotFound for ClusterPermission, got err=%v", err)

	// The managed service account must be in managedCluster namespace and name should be "-mtv" suffix
	_, err = dynClient.Resource(ManagedServiceAccountsGVR).Namespace("test-cluster").Get(
		context.TODO(), "test-cluster-mtv", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected NotFound for ManagedServiceAccount, got err=%v", err)

	// The provider secret must be in mtv-integrations namespace and name should be "-mtv" suffix
	_, err = dynClient.Resource(ProviderSecretGVR).Namespace(MTVIntegrationsNamespace).Get(
		context.TODO(), "test-cluster-mtv", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected NotFound for Provider Secret, got err=%v", err)

	// The provider must be in mtv-integrations namespace and name should be "-mtv" suffix
	_, err = dynClient.Resource(ProvidersGVR).Namespace(MTVIntegrationsNamespace).Get(
		context.TODO(), "test-cluster-mtv", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected NotFound for Provider, got err=%v", err)

	// Also verify the finalizer is removed.
	updated := &clusterv1.ManagedCluster{}
	_ = k8sClient.Get(context.TODO(), types.NamespacedName{Name: "test-cluster"}, updated)
	assert.NotContains(t, updated.Finalizers, ManagedClusterFinalizer)
}

func TestManagedClusterReconciler_checkProviderCRD(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: ProviderCRDName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{
					Type:   apiextensionsv1.Established,
					Status: apiextensionsv1.ConditionFalse,
				},
			},
		},
	}

	t.Run("CRD does not exist", func(t *testing.T) {
		client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ManagedClusterReconciler{Client: client, Scheme: scheme}
		ok, err := r.checkProviderCRD(context.Background())
		require.NoError(t, err)
		require.False(t, ok, "Should return false when CRD does not exist")
	})

	t.Run("CRD exists but not established", func(t *testing.T) {
		client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
		r := &ManagedClusterReconciler{Client: client, Scheme: scheme}
		ok, err := r.checkProviderCRD(context.Background())
		require.NoError(t, err)
		require.False(t, ok, "Should return false when CRD exists but is not established")
	})

	t.Run("CRD exists and is established", func(t *testing.T) {
		client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(providerCrd).Build()
		r := &ManagedClusterReconciler{Client: client, Scheme: scheme}
		ok, err := r.checkProviderCRD(context.Background())
		require.NoError(t, err)
		require.True(t, ok, "Should return true when CRD is established")
	})
}

// Additional tests to increase coverage

func TestReconcile_SkipsWhenCRDNotEstablished(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	// Create a CRD that is not established
	notEstablishedCrd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: ProviderCRDName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{
					Type:   apiextensionsv1.Established,
					Status: apiextensionsv1.ConditionFalse,
				},
			},
		},
	}

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-cluster",
			Labels: map[string]string{LabelCNVOperatorInstall: "true"},
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(notEstablishedCrd, managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	result, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_HandlesClusterWithoutLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	managedCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
			// No CNV operator install label
		},
	}

	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(providerCrd, managedCluster).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	result, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-cluster"}})

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_ErrorHandling(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	// Test with non-existent cluster
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(providerCrd).Build()
	dynClient := fake.NewSimpleDynamicClient(scheme)

	reconciler := &ManagedClusterReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		DynamicClient: dynClient,
	}

	result, err := reconciler.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "non-existent-cluster"}})

	assert.NoError(t, err) // Should not error on NotFound
	assert.Equal(t, ctrl.Result{}, result)
}

func TestHelperMethods(t *testing.T) {
	// Test shouldCleanupCluster
	t.Run("shouldCleanupCluster", func(t *testing.T) {
		reconciler := &ManagedClusterReconciler{}

		// Test with deletion timestamp
		clusterWithDeletion := &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test",
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Finalizers:        []string{ManagedClusterFinalizer},
			},
		}
		assert.True(t, reconciler.shouldCleanupCluster(clusterWithDeletion))

		// Test without label but with finalizer
		clusterWithoutLabel := &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test",
				Finalizers: []string{ManagedClusterFinalizer},
			},
		}
		assert.True(t, reconciler.shouldCleanupCluster(clusterWithoutLabel))

		// Test with label and no finalizer
		clusterWithLabel := &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "test",
				Labels: map[string]string{LabelCNVOperatorInstall: "true"},
			},
		}
		assert.False(t, reconciler.shouldCleanupCluster(clusterWithLabel))
	})

	// Test shouldManageCluster
	t.Run("shouldManageCluster", func(t *testing.T) {
		reconciler := &ManagedClusterReconciler{}

		clusterWithLabel := &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{LabelCNVOperatorInstall: "true"},
			},
		}
		assert.True(t, reconciler.shouldManageCluster(clusterWithLabel))

		clusterWithoutLabel := &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{},
			},
		}
		assert.False(t, reconciler.shouldManageCluster(clusterWithoutLabel))
	})
}

func TestSecretNeedsUpdate(t *testing.T) {
	reconciler := &ManagedClusterReconciler{}

	// Test when secrets are different
	secret1 := &corev1.Secret{
		Data: map[string][]byte{
			"cacert": []byte("cert1"),
			"token":  []byte("token1"),
		},
	}

	secret2 := &corev1.Secret{
		Data: map[string][]byte{
			"ca.crt": []byte("cert2"),
			"token":  []byte("token2"),
		},
	}

	assert.True(t, reconciler.secretNeedsUpdate(secret1, secret2))

	// Test when secrets are the same
	secret3 := &corev1.Secret{
		Data: map[string][]byte{
			"cacert": []byte("cert1"),
			"token":  []byte("token1"),
		},
	}

	secret4 := &corev1.Secret{
		Data: map[string][]byte{
			"ca.crt": []byte("cert1"),
			"token":  []byte("token1"),
		},
	}

	assert.False(t, reconciler.secretNeedsUpdate(secret3, secret4))
}
