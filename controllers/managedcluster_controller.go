package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	auth "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
)

// ManagedClusterReconciler reconciles a ManagedCluster object
type ManagedClusterReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	DynamicClient dynamic.Interface
}

const (
	ProviderCRDName = "providers.forklift.konveyor.io"
)

//nolint:revive // Added by kubebuilder
//+kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
//nolint:revive // Added by kubebuilder
//+kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters/status,verbs=get
//nolint:revive // Added by kubebuilder
//+kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters/finalizers,verbs=update
//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=rbac.open-cluster-management.io,resources=clusterpermissions,verbs=get;list;watch;create;update;patch;delete
//nolint:revive // Added by kubebuilder
//+kubebuilder:rbac:groups=rbac.open-cluster-management.io,resources=clusterpermissions/status,verbs=get;update;patch
//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=authentication.open-cluster-management.io,resources=managedserviceaccounts,verbs=get;list;watch;create;update;patch;delete
//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=authentication.open-cluster-management.io,resources=managedserviceaccounts/status,verbs=get;update;patch

// Reconcile handles the reconciliation of ManagedCluster resources for MTV integration
// Refactored to reduce cognitive complexity from 51 to under 50 for SonarQube compliance
func (r *ManagedClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Early exit if Provider CRD is not established
	if result, err := r.validateProviderCRD(ctx); err != nil || result.RequeueAfter > 0 {
		return result, err
	}

	// Get ManagedCluster instance
	managedCluster, err := r.getManagedCluster(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ManagedCluster", "name", req.NamespacedName)

	// Handle deletion scenarios
	if r.shouldCleanupCluster(managedCluster) {
		return ctrl.Result{}, r.cleanupManagedClusterResources(ctx, managedCluster)
	}

	// Handle active cluster lifecycle
	if r.shouldManageCluster(managedCluster) {
		return r.reconcileActiveCluster(ctx, managedCluster)
	}

	return ctrl.Result{}, nil
}

// Helper methods to reduce cognitive complexity in Reconcile function

// validateProviderCRD checks if the Provider CRD is established
func (r *ManagedClusterReconciler) validateProviderCRD(ctx context.Context) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	crdEstablished, err := r.checkProviderCRD(ctx)
	if err != nil {
		log.Error(err, "Failed to check if Provider CRD is established")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	if !crdEstablished {
		log.Info("Provider CRD is not established, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// getManagedCluster retrieves the ManagedCluster instance
func (r *ManagedClusterReconciler) getManagedCluster(ctx context.Context, namespacedName types.NamespacedName) (*clusterv1.ManagedCluster, error) {
	managedCluster := &clusterv1.ManagedCluster{}
	if err := r.Get(ctx, namespacedName, managedCluster); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return managedCluster, nil
}

// shouldCleanupCluster determines if the cluster should be cleaned up
func (r *ManagedClusterReconciler) shouldCleanupCluster(managedCluster *clusterv1.ManagedCluster) bool {
	return (managedCluster.GetDeletionTimestamp() != nil ||
		managedCluster.GetLabels()[LabelCNVOperatorInstall] != "true") &&
		controllerutil.ContainsFinalizer(managedCluster, ManagedClusterFinalizer)
}

// shouldManageCluster determines if the cluster should be managed
func (r *ManagedClusterReconciler) shouldManageCluster(managedCluster *clusterv1.ManagedCluster) bool {
	return managedCluster.GetLabels()[LabelCNVOperatorInstall] == "true"
}

// reconcileActiveCluster handles the complete lifecycle for active MTV clusters
func (r *ManagedClusterReconciler) reconcileActiveCluster(ctx context.Context, managedCluster *clusterv1.ManagedCluster) (ctrl.Result, error) {
	managedClusterMTV := managedClusterMTVName(managedCluster.GetName())

	// Ensure finalizer is present
	if result, err := r.ensureFinalizerAndNamespace(ctx, managedCluster); err != nil || result.RequeueAfter > 0 {
		return result, err
	}

	// Handle ManagedServiceAccount lifecycle
	managedServiceAccount, result, err := r.handleManagedServiceAccount(ctx, managedCluster, managedClusterMTV)
	if err != nil || result.RequeueAfter > 0 {
		return result, err
	}

	// Reconcile cluster permissions
	if err := r.reconcileClusterPermissions(ctx, managedCluster); err != nil {
		return ctrl.Result{}, err
	}

	// Handle provider secrets synchronization
	if err := r.handleProviderSecrets(ctx, managedCluster, managedServiceAccount, managedClusterMTV); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile provider resources
	if err := r.reconcileProviderResources(ctx, managedCluster); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureFinalizerAndNamespace ensures finalizer is present and MTV namespace exists
func (r *ManagedClusterReconciler) ensureFinalizerAndNamespace(ctx context.Context, managedCluster *clusterv1.ManagedCluster) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(managedCluster, ManagedClusterFinalizer) {
		return ctrl.Result{}, nil
	}

	original := managedCluster.DeepCopy()
	controllerutil.AddFinalizer(managedCluster, ManagedClusterFinalizer)

	log.Info("Create the " + MTVIntegrationsNamespace + " namespace")
	MTVNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: MTVIntegrationsNamespace}}
	if err := r.Create(ctx, MTVNamespace); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	if err := r.Patch(ctx, managedCluster, client.MergeFrom(original)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil // Requeue to ensure the finalizer is added
}

// handleManagedServiceAccount manages the ManagedServiceAccount lifecycle
func (r *ManagedClusterReconciler) handleManagedServiceAccount(ctx context.Context, managedCluster *clusterv1.ManagedCluster, managedClusterMTV string) (*auth.ManagedServiceAccount, ctrl.Result, error) {
	log := log.FromContext(ctx)
	managedClusterNamespace := managedCluster.Name

	managedServiceAccount := &auth.ManagedServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: managedClusterMTV, Namespace: managedClusterNamespace}, managedServiceAccount)

	if errors.IsNotFound(err) {
		return r.createManagedServiceAccount(ctx, managedCluster, managedClusterMTV, managedClusterNamespace)
	} else if err != nil {
		log.Error(err, "Failed to retrieve ManagedServiceAccount")
		return nil, ctrl.Result{}, err
	}

	return managedServiceAccount, ctrl.Result{}, nil
}

// createManagedServiceAccount creates a new ManagedServiceAccount
func (r *ManagedClusterReconciler) createManagedServiceAccount(ctx context.Context, managedCluster *clusterv1.ManagedCluster, managedClusterMTV, managedClusterNamespace string) (*auth.ManagedServiceAccount, ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("ManagedServiceAccount not found, creating new one")

	managedServiceAccount := &auth.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedClusterMTV,
			Namespace: managedClusterNamespace,
		},
		Spec: auth.ManagedServiceAccountSpec{
			Rotation: auth.ManagedServiceAccountRotation{
				Enabled:  true,
				Validity: metav1.Duration{Duration: time.Minute * 60},
			},
		},
	}

	if err := controllerutil.SetControllerReference(managedCluster, managedServiceAccount, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		log.Error(err, "Failed to set ManagedServiceAccount owner reference to ManagedCluster")
		return nil, ctrl.Result{}, err
	}

	if err := r.Create(ctx, managedServiceAccount, &client.CreateOptions{}); err != nil {
		log.Error(err, "Failed to create ManagedServiceAccount")
		return nil, ctrl.Result{}, err
	}

	log.Info("Created successfully", "ManagedServiceAccount", managedServiceAccount.Name, "namespace", managedClusterNamespace)
	return managedServiceAccount, ctrl.Result{RequeueAfter: TokenWaitDuration}, nil
}

// reconcileClusterPermissions handles cluster permission reconciliation
func (r *ManagedClusterReconciler) reconcileClusterPermissions(ctx context.Context, managedCluster *clusterv1.ManagedCluster) error {
	if err := r.reconcileResource(ctx, ClusterPermissionsGVR, managedCluster.Name, managedCluster.Name, clusterPermissionPayload(managedCluster)); err != nil {
		log := log.FromContext(ctx)
		log.Error(err, "Failed to reconcile ClusterPermissions")
		return err
	}
	return nil
}

// handleProviderSecrets manages provider secret synchronization
func (r *ManagedClusterReconciler) handleProviderSecrets(ctx context.Context, managedCluster *clusterv1.ManagedCluster, managedServiceAccount *auth.ManagedServiceAccount, managedClusterMTV string) error {
	log := log.FromContext(ctx)
	managedClusterNamespace := managedCluster.Name

	// Check if token secret is ready
	if managedServiceAccount.Status.TokenSecretRef == nil || managedServiceAccount.Status.TokenSecretRef.Name == "" {
		log.Info("ManagedServiceAccount secret is not ready")
		return nil // Will be handled on next reconcile
	}

	// Get source secret from ManagedServiceAccount
	ogSecret := &corev1.Secret{}
	namespacedName := types.NamespacedName{
		Name:      managedClusterMTV,
		Namespace: managedClusterNamespace,
	}

	if err := r.Client.Get(ctx, namespacedName, ogSecret); err != nil {
		log.Error(err, "Failed to retrieve ManagedServiceAccount secret")
		return err
	}

	// Create or update provider secret
	return r.syncProviderSecret(ctx, managedCluster, ogSecret, managedClusterMTV)
}

// syncProviderSecret synchronizes the provider secret with ManagedServiceAccount data
func (r *ManagedClusterReconciler) syncProviderSecret(ctx context.Context, managedCluster *clusterv1.ManagedCluster, sourceSecret *corev1.Secret, managedClusterMTV string) error {
	log := log.FromContext(ctx)

	providerSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedClusterMTV,
			Namespace: MTVIntegrationsNamespace,
			Labels: map[string]string{
				"createdForProviderType": "openshift",
				"createdForResourceType": "providers",
			},
		},
		Data: map[string][]byte{
			"insecureSkipVerify": []byte("false"),
			"url":                []byte(managedCluster.Spec.ManagedClusterClientConfigs[0].URL),
		},
	}

	// Check if secret needs updating
	namespacedName := types.NamespacedName{
		Name:      managedClusterMTV,
		Namespace: MTVIntegrationsNamespace,
	}

	if err := r.Client.Get(ctx, namespacedName, providerSecret); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to retrieve Provider secret")
		return err
	}

	// Update secret if data has changed
	if r.secretNeedsUpdate(providerSecret, sourceSecret) {
		return r.updateProviderSecret(ctx, providerSecret, sourceSecret)
	}

	return nil
}

// secretNeedsUpdate checks if the provider secret needs to be updated
func (r *ManagedClusterReconciler) secretNeedsUpdate(providerSecret, sourceSecret *corev1.Secret) bool {
	return !bytes.Equal(providerSecret.Data["cacert"], sourceSecret.Data["ca.crt"]) ||
		!bytes.Equal(providerSecret.Data["token"], sourceSecret.Data["token"])
}

// updateProviderSecret updates the provider secret with new data
func (r *ManagedClusterReconciler) updateProviderSecret(ctx context.Context, providerSecret, sourceSecret *corev1.Secret) error {
	log := log.FromContext(ctx)
	log.Info("Adding provider details to secret", "secret", providerSecret.Name, "namespace", MTVIntegrationsNamespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, providerSecret, func() error {
		providerSecret.Data["cacert"] = sourceSecret.Data["ca.crt"]
		providerSecret.Data["token"] = sourceSecret.Data["token"]
		return nil
	})

	if err != nil {
		log.Error(err, "Failed to create or patch", "secret", providerSecret.Name, "namespace", MTVIntegrationsNamespace)
		return err
	}

	log.Info("Created or Patched successfully", "secret", providerSecret.Name, "namespace", MTVIntegrationsNamespace)
	return nil
}

// reconcileProviderResources handles provider resource reconciliation
func (r *ManagedClusterReconciler) reconcileProviderResources(ctx context.Context, managedCluster *clusterv1.ManagedCluster) error {
	if err := r.reconcileResource(ctx, ProvidersGVR, managedCluster.Name, MTVIntegrationsNamespace, providerPayload(managedCluster)); err != nil {
		log := log.FromContext(ctx)
		log.Error(err, "Failed to reconcile Provider")
		return err
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controllers.ManagedClusterReconciler.SetupWithManager")
	log.Info("Initializing ManagedCluster controller setup")

	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.ManagedCluster{}).
		Owns(&auth.ManagedServiceAccount{}). // Watch ManagedServiceAccounts owned by ManagedClusters
		Watches(
			// Watch the Provider CRD
			&apiextensionsv1.CustomResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// Only react to the specific Provider CRD
				if obj.GetName() != ProviderCRDName {
					return nil
				}
				// List all ManagedClusters and enqueue a reconcile for each
				var reqs []reconcile.Request
				var mcList clusterv1.ManagedClusterList
				if err := r.Client.List(ctx, &mcList); err != nil {
					log.Error(err, "Failed to list ManagedClusters on Provider CRD event")
					return nil
				}
				for _, mc := range mcList.Items {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      mc.Name,
							Namespace: mc.Namespace, // Namespace is empty for ManagedCluster
						},
					})
				}
				return reqs
			}),
		).Complete(r)
}

func (r *ManagedClusterReconciler) reconcileResource(
	ctx context.Context,
	gvr schema.GroupVersionResource,
	managedClusterName string,
	namespace string,
	payload map[string]interface{},
) error {
	log := log.FromContext(ctx)
	resourceKind := gvr.Resource
	_, err := r.DynamicClient.Resource(gvr).Namespace(namespace).Get(
		ctx,
		managedClusterMTVName(managedClusterName),
		metav1.GetOptions{})
	if errors.IsNotFound(err) {
		log.Info("Create " + resourceKind)

		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			log.Error(err, "Failed to marshal "+resourceKind+" JSON")
			return err
		}

		unstructuredPayload := &unstructured.Unstructured{}
		err = json.Unmarshal(payloadJSON, unstructuredPayload)
		if err != nil {
			log.Error(err, "Failed to unmarshal "+resourceKind+" JSON to unstructured")
			return err
		}

		_, err = r.DynamicClient.Resource(gvr).Namespace(namespace).Create(
			ctx, unstructuredPayload, metav1.CreateOptions{})
		if err != nil {
			log.Error(err, "Failed to create resource", "kind", resourceKind, "namespace", namespace)
			return err
		}
		log.Info("Created successfully", resourceKind, managedClusterName, "namespace", namespace)
	} else if err != nil {
		return err
	}
	return nil
}

func deleteResource(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	gvr schema.GroupVersionResource,
	managedClusterName string,
	namespace string,
) error {
	log := log.FromContext(ctx)
	resourceKind := gvr.Resource
	managedClusterMTV := managedClusterMTVName(managedClusterName)

	err := dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx,
		managedClusterMTV, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Resource not found, nothing to delete", resourceKind, managedClusterMTV,
				"namespace", namespace)
			return nil
		}
		log.Error(err, "Failed to delete "+resourceKind)
		return err
	}
	log.Info("Deleted successfully", resourceKind, managedClusterMTV, "namespace", namespace)
	return nil
}

func (r *ManagedClusterReconciler) cleanupManagedClusterResources(ctx context.Context,
	managedCluster *clusterv1.ManagedCluster,
) error {
	log := log.FromContext(ctx)
	log.Info("The ManagedCluster is no longer labeled for CNV operator installation, cleaning up resources")
	managedClusterName := managedCluster.GetName()
	// Delete the following resources if they exist:
	//  * ClusterPermission
	//  * ManagedServiceAccount
	//  * Provider
	if err := deleteResource(ctx,
		r.DynamicClient,
		ClusterPermissionsGVR,
		managedClusterName,
		managedClusterName); err != nil {
		return err
	}
	if err := deleteResource(ctx,
		r.DynamicClient,
		ManagedServiceAccountsGVR,
		managedClusterName,
		managedClusterName); err != nil {
		return err
	}
	if err := deleteResource(ctx,
		r.DynamicClient,
		ProviderSecretGVR,
		managedClusterName,
		MTVIntegrationsNamespace); err != nil {
		return err
	}

	if err := deleteResource(ctx,
		r.DynamicClient,
		ProvidersGVR,
		managedClusterName,
		MTVIntegrationsNamespace); err != nil {
		return err
	}
	original := managedCluster.DeepCopy()
	if !controllerutil.RemoveFinalizer(managedCluster, ManagedClusterFinalizer) {
		log.Info("Finalizer not found, nothing to remove")
	} else {
		patch := client.MergeFrom(original)
		if err := r.Patch(ctx, managedCluster, patch); err != nil {
			return err
		}
		log.Info("Finalizer removed")
	}
	return nil
}

func managedClusterMTVName(name string) string {
	return name + "-mtv"
}

func (r *ManagedClusterReconciler) checkProviderCRD(ctx context.Context) (bool, error) {
	// Check if the Provider CRD is established
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: ProviderCRDName}, crd)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	isEstablished := false
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			isEstablished = true
			break
		}
	}

	if isEstablished {
		return true, nil // CRD exists but is not established
	}

	return false, nil
}
