package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	v1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var kubevirtprojectsClusterViewGVR = schema.GroupVersionResource{
	Group:    "clusterview.open-cluster-management.io",
	Version:  "v1",
	Resource: "kubevirtprojects",
}

func ValidateWebhook(c client.Client, config rest.Config) *webhook.Admission {
	return &webhook.Admission{
		Handler: admission.HandlerFunc(func(ctx context.Context, req webhook.AdmissionRequest) webhook.AdmissionResponse {
			log := ctrl.LoggerFrom(ctx).WithValues("operation", req.Operation, "user", req.UserInfo.Username)
			if req.Operation == v1.Create || req.Operation == v1.Update {
				if len(req.Object.Raw) == 0 {
					return webhook.Denied("Request object is empty")
				}

				plan, err := rawToPlan(req.Object)
				if plan == nil || err != nil {
					log.Error(err, "Failed to parse request object into Plan")
					return webhook.Denied("Failed to parse request object into Plan")
				}

				targetNamespace := plan.Spec.TargetNamespace
				destinationName := plan.Spec.Provider.Destination.Name

				if !strings.HasSuffix(destinationName, "-mtv") {
					log.Info("Skipping Plan validation: destination provider does not have MTV-managed suffix",
						"destinationProvider", destinationName)
					return webhook.Allowed("Plan validation skipped: destination provider is not managed by MTV controller")
				}

				clusterName := strings.TrimSuffix(destinationName, "-mtv")

				log = log.WithValues("cluster", clusterName, "namespace", targetNamespace)

				config.Impersonate = rest.ImpersonationConfig{
					UserName: req.UserInfo.Username,
					Groups:   req.UserInfo.Groups,
					UID:      req.UserInfo.UID,
				}

				dynamicClient, err := dynamic.NewForConfig(&config)
				if err != nil {
					log.Error(err, "Failed to initialize dynamic client with impersonation")
					return webhook.Denied("Failed to setup dynamic client")
				}

				valid, err := validateKubevirtView(dynamicClient, clusterName, targetNamespace)
				if err != nil {
					log.Error(err, "Validation failed during access check")
					return webhook.Denied("Authorization check for cluster access failed")
				}

				if !valid {
					return webhook.Denied(fmt.Sprintf("User does not have permission to access "+
						"the target namespace: %s in cluster: %s",
						targetNamespace, clusterName))
				}
			}

			return webhook.Allowed("Plan validation passed")
		}),
	}
}

func rawToPlan(rawExt runtime.RawExtension) (*v1beta1.Plan, error) {
	if len(rawExt.Raw) == 0 {
		return nil, nil
	}

	plan := &v1beta1.Plan{}
	if err := json.Unmarshal(rawExt.Raw, plan); err != nil {
		return nil, err
	}

	return plan, nil
}

func validateKubevirtView(dynamicClient dynamic.Interface, targetCluster, targetNamespace string) (bool, error) {
	virtProjectList, err := dynamicClient.Resource(kubevirtprojectsClusterViewGVR).
		List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("validateClusterView: failed to List Resource %v", err)
	}

	for _, item := range virtProjectList.Items {
		cluster, _, err := unstructured.NestedString(item.Object, "metadata", "labels", "cluster")
		if err != nil {
			return false, fmt.Errorf("Failed to extract 'cluster' label: %v", err)
		}

		// project is namespace
		namespace, _, err := unstructured.NestedString(item.Object, "metadata", "labels", "project")
		if err != nil {
			return false, fmt.Errorf("Failed to extract 'project' (namespace) label: %v", err)
		}

		if cluster == targetCluster && (namespace == targetNamespace || namespace == "all_projects") {
			return true, nil
		}
	}

	return false, nil
}
