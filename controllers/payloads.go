// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package controllers

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

const (
	ManagedClusterFinalizer  = "mtv-integrations.open-cluster-management.io/resource-cleanup"
	LabelCNVOperatorInstall  = "acm/cnv-operator-install"
	MTVIntegrationsNamespace = "mtv-integrations"
	payloadKeyAPIVersion     = "apiVersion"
	payloadKeyKind           = "kind"
	payloadKeyMetadata       = "metadata"
	payloadKeyName           = "name"
	payloadKeyNamespace      = "namespace"
	payloadKeyURL            = "url"
	payloadKeySpec           = "spec"

	clusterPermissionAPIVersion = "rbac.open-cluster-management.io/v1alpha1"
	clusterPermissionKind       = "ClusterPermission"
)

var TokenWaitDuration = 4 * time.Second

var (
	ClusterPermissionsGVR     = generateGVR("rbac.open-cluster-management.io", "v1alpha1", "clusterpermissions")
	ManagedServiceAccountsGVR = generateGVR(
		"authentication.open-cluster-management.io",
		"v1beta1",
		"managedserviceaccounts")
)
var (
	ProvidersGVR      = generateGVR("forklift.konveyor.io", "v1beta1", "providers")
	ProviderSecretGVR = generateGVR("", "v1", "secrets")
)

func providerPayload(managedCluster *clusterv1.ManagedCluster) map[string]interface{} {
	managedClusterMTV := managedCluster.Name + "-mtv"

	// Safely get the URL from ManagedClusterClientConfigs
	var clusterURL string
	if len(managedCluster.Spec.ManagedClusterClientConfigs) > 0 {
		clusterURL = managedCluster.Spec.ManagedClusterClientConfigs[0].URL
	}

	return map[string]interface{}{
		payloadKeyAPIVersion: "forklift.konveyor.io/v1beta1",
		payloadKeyKind:       "Provider",
		payloadKeyMetadata: map[string]interface{}{
			payloadKeyName:      managedClusterMTV,
			payloadKeyNamespace: MTVIntegrationsNamespace,
		},
		payloadKeySpec: map[string]interface{}{
			"type":        "openshift",
			payloadKeyURL: clusterURL,
			"secret": map[string]interface{}{
				payloadKeyName:      managedClusterMTV,
				payloadKeyNamespace: MTVIntegrationsNamespace,
			},
		},
	}
}

func clusterPermissionPayload(managedCluster *clusterv1.ManagedCluster, msaaNamespace string) map[string]interface{} {
	managedClusterMTV := managedCluster.Name + "-mtv"
	return map[string]interface{}{
		payloadKeyAPIVersion: clusterPermissionAPIVersion,
		payloadKeyKind:       clusterPermissionKind,
		payloadKeyMetadata: map[string]interface{}{
			payloadKeyName:      managedClusterMTV,
			payloadKeyNamespace: managedCluster.Name,
		},
		payloadKeySpec: map[string]interface{}{
			"clusterRoleBinding": map[string]interface{}{
				"subject": map[string]interface{}{
					payloadKeyKind:      "ServiceAccount",
					payloadKeyName:      managedClusterMTV,
					payloadKeyNamespace: msaaNamespace, // The ServiceAccount is created here on the ManagedCluster
				},
				"roleRef": map[string]interface{}{ // This is the documented RBAC for the MTV Provider
					payloadKeyKind: "ClusterRole",
					payloadKeyName: "cluster-admin",
					"apiGroup":     "rbac.authorization.k8s.io",
				},
			},
		},
	}
}

// advisorClusterPermissionPayload grants the hub mtv-integrations-manager SA
// acm-vm-extended:admin on the spoke for ACM Search API impersonation.
func advisorClusterPermissionPayload(
	managedCluster *clusterv1.ManagedCluster,
	managerNamespace string,
) map[string]interface{} {
	managedClusterMTVAdvisor := managedCluster.Name + "-mtv-advisor"
	return map[string]interface{}{
		payloadKeyAPIVersion: clusterPermissionAPIVersion,
		payloadKeyKind:       clusterPermissionKind,
		payloadKeyMetadata: map[string]interface{}{
			payloadKeyName:      managedClusterMTVAdvisor,
			payloadKeyNamespace: managedCluster.Name,
		},
		payloadKeySpec: map[string]interface{}{
			"clusterRoleBinding": map[string]interface{}{
				"subject": map[string]interface{}{
					payloadKeyKind:      "ServiceAccount",
					payloadKeyName:      "mtv-integrations-manager",
					payloadKeyNamespace: managerNamespace,
				},
				"roleRef": map[string]interface{}{
					payloadKeyKind: "ClusterRole",
					payloadKeyName: "acm-vm-extended:admin",
					"apiGroup":     "rbac.authorization.k8s.io",
				},
			},
		},
	}
}

func generateGVR(group string, version string, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}
}
