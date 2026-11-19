// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

// advisorUserPermissionGVR is the GVR for ACM clusterview UserPermission objects.
var advisorUserPermissionGVR = schema.GroupVersionResource{
	Group:    "clusterview.open-cluster-management.io",
	Version:  "v1alpha1",
	Resource: "userpermissions",
}

const (
	hubClusterName = "local-cluster"

	// Default UserPermission resource names. Standard Kubernetes rejects ':' in
	// metadata.name, so kind/e2e tests override these via MTV_ADVISOR_ROLE_VM_FLEET_ADMIN
	// MTV_ADVISOR_ROLE_CLUSTER_ADMIN, and MTV_ADVISOR_ROLE_KUBEVIRT_ADMIN with DNS-safe alternatives.
	defaultRoleVMFleetAdmin        = "acm-vm-fleet:admin"
	defaultRoleKubevirtAdmin       = "kubevirt.io:admin"
	defaultRoleManagedClusterAdmin = "managedcluster:admin"
)

// advisorRoleVMFleetAdmin returns the UserPermission name for the fleet-admin
// role, overridable via MTV_ADVISOR_ROLE_VM_FLEET_ADMIN for kind/e2e.
func advisorRoleVMFleetAdmin() string {
	if v := strings.TrimSpace(os.Getenv("MTV_ADVISOR_ROLE_VM_FLEET_ADMIN")); v != "" {
		return v
	}
	return defaultRoleVMFleetAdmin
}

// advisorRoleManagedClusterAdmin returns the UserPermission name for the
// cluster-admin role, overridable via MTV_ADVISOR_ROLE_CLUSTER_ADMIN for kind/e2e.
func advisorRoleManagedClusterAdmin() string {
	if v := strings.TrimSpace(os.Getenv("MTV_ADVISOR_ROLE_CLUSTER_ADMIN")); v != "" {
		return v
	}
	return defaultRoleManagedClusterAdmin
}

// advisorRoleKubevirtAdmin returns the UserPermission name for kubevirt admin on spokes.
func advisorRoleKubevirtAdmin() string {
	if v := strings.TrimSpace(os.Getenv("MTV_ADVISOR_ROLE_KUBEVIRT_ADMIN")); v != "" {
		return v
	}
	return defaultRoleKubevirtAdmin
}

// CheckCallerAuthorizedForClusters returns true when the caller may invoke proxy-cclm CA exchange:
// hub acm-vm-fleet:admin (local-cluster with namespaces *) and kubevirt.io:admin bindings for every
// named cluster. Spoke API calls still enforce RBAC via the managed-cluster proxy (403/502 on denial).
// Uses the caller's bearer token (not the controller SA).
// Used by proxy-cclm CA exchange only; migration advisor keeps the looser hub check.
func CheckCallerAuthorizedForClusters(
	ctx context.Context,
	controllerClient dynamic.Interface,
	baseConfig *rest.Config,
	bearerToken string,
	clusters []string,
) (bool, error) {
	if len(clusters) == 0 {
		return false, nil
	}
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	callerClient, err := newCallerDynamicClient(baseConfig, bearerToken)
	if err != nil {
		return false, err
	}

	ok, err := userPermissionCoversHubWildcard(authCtx, callerClient)
	if err != nil || !ok {
		return ok, err
	}

	return callerCoversClustersForRole(
		authCtx, callerClient, clusters, advisorRoleKubevirtAdmin(),
		"authorization denied: caller lacks kubevirt.io:admin",
	)
}

func callerCoversClusters(ctx context.Context, callerClient dynamic.Interface, clusters []string) (bool, error) {
	return callerCoversClustersForRole(
		ctx, callerClient, clusters, advisorRoleManagedClusterAdmin(),
		"authorization denied: caller lacks managedcluster:admin",
	)
}

func callerCoversClustersForRole(
	ctx context.Context,
	callerClient dynamic.Interface,
	clusters []string,
	roleName string,
	deniedLog string,
) (bool, error) {
	if len(clusters) == 0 {
		return false, nil
	}
	obj, err := callerClient.Resource(advisorUserPermissionGVR).Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get UserPermission %q: %w", roleName, err)
	}

	covered := make(map[string]struct{})
	bindings, found, err := unstructured.NestedSlice(obj.Object, "status", "bindings")
	if err != nil {
		return false, fmt.Errorf("decode UserPermission %q bindings: %w", roleName, err)
	}
	if found {
		for _, raw := range bindings {
			b, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			cluster, _, err := unstructured.NestedString(b, "cluster")
			if err != nil {
				return false, err
			}
			covered[cluster] = struct{}{}
		}
	}

	log := ctrl.LoggerFrom(ctx)
	for _, cluster := range clusters {
		if _, ok := covered[cluster]; !ok {
			log.Info(deniedLog, "cluster", cluster)
			return false, nil
		}
	}
	return true, nil
}

// checkCallerAuthorized returns true when the bearer-token caller holds either:
//  1. acm-vm-fleet:admin with a local-cluster binding, or
//  2. managedcluster:admin covering every CNV-enabled managed cluster.
//
// A separate dynamic client is built from the caller's own token so RBAC is
// evaluated as the calling user, not the controller SA.
func checkCallerAuthorized(
	ctx context.Context,
	controllerClient dynamic.Interface,
	baseConfig *rest.Config,
	bearerToken string,
) (bool, error) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	callerClient, err := newCallerDynamicClient(baseConfig, bearerToken)
	if err != nil {
		return false, err
	}

	if ok, err := userPermissionCoversHub(authCtx, callerClient); err != nil || ok {
		return ok, err
	}

	return callerIsClusterAdmin(authCtx, controllerClient, callerClient)
}

func newCallerDynamicClient(baseConfig *rest.Config, bearerToken string) (dynamic.Interface, error) {
	callerCfg := rest.CopyConfig(baseConfig)
	callerCfg.BearerToken = bearerToken
	callerCfg.BearerTokenFile = ""
	callerCfg.Impersonate = rest.ImpersonationConfig{}
	callerCfg.CertFile = ""
	callerCfg.KeyFile = ""
	callerCfg.CertData = nil
	callerCfg.KeyData = nil
	client, err := dynamic.NewForConfig(callerCfg)
	if err != nil {
		return nil, fmt.Errorf("build caller dynamic client: %w", err)
	}
	return client, nil
}

// userPermissionCoversHub returns true when the caller's acm-vm-fleet:admin
// UserPermission has a local-cluster binding.
func userPermissionCoversHub(ctx context.Context, callerClient dynamic.Interface) (bool, error) {
	return userPermissionHubBinding(ctx, callerClient, false)
}

// userPermissionCoversHubWildcard requires local-cluster and namespaces * (proxy-cclm CA exchange).
func userPermissionCoversHubWildcard(ctx context.Context, callerClient dynamic.Interface) (bool, error) {
	return userPermissionHubBinding(ctx, callerClient, true)
}

func userPermissionHubBinding(
	ctx context.Context,
	callerClient dynamic.Interface,
	requireWildcardNamespaces bool,
) (bool, error) {
	roleName := advisorRoleVMFleetAdmin()
	obj, err := callerClient.Resource(advisorUserPermissionGVR).Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get UserPermission %q: %w", roleName, err)
	}

	bindings, found, err := unstructured.NestedSlice(obj.Object, "status", "bindings")
	if err != nil {
		return false, fmt.Errorf("decode UserPermission %q bindings: %w", roleName, err)
	}
	if !found {
		return false, nil
	}
	for _, raw := range bindings {
		binding, ok := raw.(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("decode UserPermission %q binding: unexpected %T", roleName, raw)
		}
		cluster, _, err := unstructured.NestedString(binding, "cluster")
		if err != nil {
			return false, fmt.Errorf("decode UserPermission %q binding cluster: %w", roleName, err)
		}
		if cluster != hubClusterName {
			continue
		}
		if !requireWildcardNamespaces {
			return true, nil
		}
		hasStar, err := bindingNamespacesIncludeWildcard(binding)
		if err != nil {
			return false, err
		}
		if hasStar {
			return true, nil
		}
	}
	return false, nil
}

func bindingNamespacesIncludeWildcard(binding map[string]interface{}) (bool, error) {
	nsList, found, err := unstructured.NestedStringSlice(binding, "namespaces")
	if err != nil {
		return false, fmt.Errorf("decode binding namespaces: %w", err)
	}
	if !found || len(nsList) == 0 {
		return false, nil
	}
	for _, ns := range nsList {
		if ns == "*" {
			return true, nil
		}
	}
	return false, nil
}

// callerIsClusterAdmin returns true when the caller's managedcluster:admin
// UserPermission covers every CNV-enabled managed cluster
// (acm/cnv-operator-install=true). The cluster list is fetched via the
// privileged controllerClient since the caller may lack list permission.
func callerIsClusterAdmin(
	ctx context.Context,
	controllerClient dynamic.Interface,
	callerClient dynamic.Interface,
) (bool, error) {
	cnvClusters, err := listCNVManagedClusters(ctx, controllerClient)
	if err != nil {
		return false, fmt.Errorf("list CNV managed clusters: %w", err)
	}
	if len(cnvClusters) == 0 {
		return false, nil
	}

	roleName := advisorRoleManagedClusterAdmin()
	obj, err := callerClient.Resource(advisorUserPermissionGVR).Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get UserPermission %q: %w", roleName, err)
	}

	covered := make(map[string]struct{})
	bindings, found, err := unstructured.NestedSlice(obj.Object, "status", "bindings")
	if err != nil {
		return false, fmt.Errorf("decode UserPermission %q bindings: %w", roleName, err)
	}
	if found {
		for _, raw := range bindings {
			b, ok := raw.(map[string]interface{})
			if !ok {
				return false, fmt.Errorf("decode UserPermission %q binding: unexpected %T", roleName, raw)
			}
			cluster, _, err := unstructured.NestedString(b, "cluster")
			if err != nil {
				return false, fmt.Errorf("decode UserPermission %q binding cluster: %w", roleName, err)
			}
			covered[cluster] = struct{}{}
		}
	}

	log := ctrl.LoggerFrom(ctx)
	for cluster := range cnvClusters {
		if _, ok := covered[cluster]; !ok {
			log.Info("authorization denied: caller lacks managedcluster:admin", "cluster", cluster)
			return false, nil
		}
	}
	return true, nil
}

// listCNVManagedClusters returns the names of ManagedClusters labelled
// acm/cnv-operator-install=true.
func listCNVManagedClusters(ctx context.Context, client dynamic.Interface) (map[string]struct{}, error) {
	list, err := client.Resource(managedClusterGVR).List(ctx, metav1.ListOptions{
		LabelSelector: cnvOperatorInstallLabel + "=true",
	})
	if err != nil {
		return nil, err
	}
	clusters := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		clusters[list.Items[i].GetName()] = struct{}{}
	}
	return clusters, nil
}
