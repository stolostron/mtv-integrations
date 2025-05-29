package webhook

import (
	"context"
	"encoding/json"

	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	v1 "k8s.io/api/admission/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func ValidateWebhook(c client.Client) *webhook.Admission {
	return &webhook.Admission{
		Handler: admission.HandlerFunc(func(ctx context.Context, req webhook.AdmissionRequest) webhook.AdmissionResponse {
			if req.Operation == v1.Create || req.Operation == v1.Update {
				if len(req.Object.Raw) == 0 {
					return webhook.Denied("Request object is empty")
				}

				plan, err := rawToPlan(req.Object)
				if plan == nil || err != nil {
					return webhook.Denied("Failed to parse request object into Plan: " + err.Error())
				}

				targetNamespace := plan.Spec.TargetNamespace

				for _, verb := range []string{"get", "list", "create"} {
					sar := &authorizationv1.SubjectAccessReview{
						Spec: authorizationv1.SubjectAccessReviewSpec{
							User:   req.UserInfo.Username,
							Groups: req.UserInfo.Groups,
							UID:    req.UserInfo.UID,
							ResourceAttributes: &authorizationv1.ResourceAttributes{
								Verb:     verb,
								Version:  "v1",
								Resource: "namespaces",
								Name:     targetNamespace,
							},
						},
					}

					err := c.Create(ctx, sar)
					if err != nil {
						return webhook.Denied("Failed to create SubjectAccessReview: " + err.Error())
					}

					// At least one of the verbs is not allowed, deny the request
					if !sar.Status.Allowed {
						return webhook.Denied("User does not have permission to access the target namespace: " +
							plan.Spec.TargetNamespace)
					}
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
