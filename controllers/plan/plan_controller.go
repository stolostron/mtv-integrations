// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package plan

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	labelCreatedBy      = "app.kubernetes.io/created-by"
	labelCCLMValue      = "cclm"
	forkliftAPIGroup    = "forklift.konveyor.io"
	forkliftAPIVersion  = "v1beta1"
	planKind            = "Plan"
)

var (
	planGVK = schema.GroupVersionKind{
		Group:   forkliftAPIGroup,
		Version: forkliftAPIVersion,
		Kind:    planKind,
	}
	planListGVK = schema.GroupVersionKind{
		Group:   forkliftAPIGroup,
		Version: forkliftAPIVersion,
		Kind:    "PlanList",
	}
	networkMapGVK = schema.GroupVersionKind{
		Group:   forkliftAPIGroup,
		Version: forkliftAPIVersion,
		Kind:    "NetworkMap",
	}
	storageMapGVK = schema.GroupVersionKind{
		Group:   forkliftAPIGroup,
		Version: forkliftAPIVersion,
		Kind:    "StorageMap",
	}
)

// PlanReconciler watches Plans labeled app.kubernetes.io/created-by=cclm and
// stamps an OwnerReference on the referenced NetworkMap and StorageMap when
// those resources also carry the same label.
type PlanReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=forklift.konveyor.io,resources=plans,verbs=get;list;watch
//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=forklift.konveyor.io,resources=networkmaps,verbs=get;list;watch;update;patch
//nolint:revive,lll // Added by kubebuilder
//+kubebuilder:rbac:groups=forklift.konveyor.io,resources=storagemaps,verbs=get;list;watch;update;patch

// Reconcile sets OwnerReferences on the NetworkMap and StorageMap referenced by
// the Plan. It is only called for Plans that have the app.kubernetes.io/created-by=cclm
// label — the watch predicate guarantees this, so no label re-check is needed here.
func (r *PlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	plan := newUnstructured(planGVK)
	if err := r.Get(ctx, req.NamespacedName, plan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling Plan", "name", plan.GetName(), "namespace", plan.GetNamespace())

	type mapRef struct {
		gvk  schema.GroupVersionKind
		name string
		ns   string
	}

	netName, _, _ := unstructured.NestedString(plan.Object, "spec", "map", "network", "name")
	netNS, _, _ := unstructured.NestedString(plan.Object, "spec", "map", "network", "namespace")
	stgName, _, _ := unstructured.NestedString(plan.Object, "spec", "map", "storage", "name")
	stgNS, _, _ := unstructured.NestedString(plan.Object, "spec", "map", "storage", "namespace")

	for _, ref := range []mapRef{
		{gvk: networkMapGVK, name: netName, ns: netNS},
		{gvk: storageMapGVK, name: stgName, ns: stgNS},
	} {
		if err := r.reconcileMapRef(ctx, plan, ref.gvk, ref.name, ref.ns); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile %s %s/%s for plan %s/%s: %w",
				ref.gvk.Kind, ref.ns, ref.name, plan.GetNamespace(), plan.GetName(), err)
		}
	}

	return ctrl.Result{}, nil
}

// reconcileMapRef resolves a single map reference (NetworkMap or StorageMap) and stamps
// an OwnerReference on it when it exists in the Plan's namespace and carries the cclm label.
func (r *PlanReconciler) reconcileMapRef(
	ctx context.Context,
	plan *unstructured.Unstructured,
	gvk schema.GroupVersionKind,
	name, ns string,
) error {
	if name == "" {
		return nil
	}

	logger := log.FromContext(ctx)
	planNS := plan.GetNamespace()

	if ns == "" {
		ns = planNS
	}
	if ns != planNS {
		logger.Info(gvk.Kind+" is in a different namespace than the Plan, skipping",
			"name", name, "mapNamespace", ns, "planNamespace", planNS)
		return nil
	}

	obj := newUnstructured(gvk)
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, obj)
	switch {
	case apierrors.IsNotFound(err):
		logger.Info(gvk.Kind+" not found, skipping", "name", name, "namespace", ns)
		return nil
	case err != nil:
		return fmt.Errorf("get %s %s/%s: %w", gvk.Kind, ns, name, err)
	case obj.GetLabels()[labelCreatedBy] != labelCCLMValue:
		logger.Info(gvk.Kind+" does not have cclm label, skipping", "name", name)
		return nil
	}

	return r.setOwner(ctx, plan, obj)
}

// setOwner stamps plan as an OwnerReference on obj. The call is idempotent —
// if the same OwnerReference already exists it is a no-op patch. If a stale
// controller ref from a re-created Plan (same Kind+Name, different UID) is
// found it is removed so the current Plan can take ownership. If a genuinely
// different controller owns the resource the call is skipped.
func (r *PlanReconciler) setOwner(ctx context.Context, plan, obj *unstructured.Unstructured) error {
	logger := log.FromContext(ctx)

	planUID := plan.GetUID()
	planName := plan.GetName()
	refs := obj.GetOwnerReferences()
	filtered := refs[:0:0] // same backing array capacity, zero length
	for _, ref := range refs {
		if ref.UID == planUID {
			return nil // already owned by this exact Plan, idempotent
		}
		if ref.Controller != nil && *ref.Controller {
			if ref.Kind == planGVK.Kind && ref.Name == planName {
				// Stale ref: same Plan name re-created with a new UID — drop it
				// so we can stamp the current Plan as owner below.
				logger.Info("removing stale Plan owner reference",
					"kind", obj.GetKind(), "name", obj.GetName(),
					"staleUID", ref.UID, "newUID", planUID)
				continue
			}
			// A genuinely different controller owns this resource; leave it alone.
			logger.Info("resource already owned by another controller, skipping",
				"kind", obj.GetKind(), "name", obj.GetName(), "owner", ref.Name)
			return nil
		}
		filtered = append(filtered, ref)
	}
	// Snapshot the original object BEFORE any mutation so the MergePatch diff
	// captures both the stale-ref removal and the new-ref addition in one patch.
	patch := client.MergeFrom(obj.DeepCopy())
	obj.SetOwnerReferences(filtered)

	isController := true
	blockOwnerDeletion := false
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion:         planGVK.Group + "/" + planGVK.Version,
		Kind:               planGVK.Kind,
		Name:               plan.GetName(),
		UID:                plan.GetUID(),
		Controller:         &isController,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}))

	if err := r.Patch(ctx, obj, patch); err != nil {
		return fmt.Errorf("patch owner reference on %s %s/%s: %w",
			obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
	}
	logger.Info("Set owner reference", "kind", obj.GetKind(), "name", obj.GetName())
	return nil
}

// fieldIndexNetworkMap and fieldIndexStorageMap are the field-index keys used
// to look up Plans by their referenced map names.
const (
	fieldIndexNetworkMap = ".spec.map.network.name"
	fieldIndexStorageMap = ".spec.map.storage.name"
)

// SetupWithManager registers the PlanReconciler with the manager.
//
// In addition to watching Plans (filtered to the cclm label), it watches
// NetworkMap and StorageMap resources that carry the same label. This
// handles the common case where the Plan is created before its maps exist:
// when a map appears later the watch re-enqueues the Plan that references it
// so the OwnerReference is stamped without waiting for an unrelated update to
// the Plan itself.
func (r *PlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	planType := newUnstructured(planGVK)

	type fieldIndex struct {
		key  string
		path []string
	}
	for _, fi := range []fieldIndex{
		{key: fieldIndexNetworkMap, path: []string{"spec", "map", "network", "name"}},
		{key: fieldIndexStorageMap, path: []string{"spec", "map", "storage", "name"}},
	} {
		if err := mgr.GetFieldIndexer().IndexField(
			context.Background(),
			planType,
			fi.key,
			func(obj client.Object) []string {
				name, _, _ := unstructured.NestedString(
					obj.(*unstructured.Unstructured).Object, fi.path...)
				if name == "" {
					return nil
				}
				return []string{name}
			},
		); err != nil {
			return err
		}
	}

	cclmPred, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{labelCreatedBy: labelCCLMValue},
	})
	if err != nil {
		return err
	}

	// mapToPlan returns the reconcile.Requests for all cclm Plans that
	// reference the changed NetworkMap or StorageMap (looked up via fieldIndex).
	mapToPlan := func(fieldIndex string) handler.MapFunc {
		return func(ctx context.Context, obj client.Object) []reconcile.Request {
			planList := &unstructured.UnstructuredList{}
			planList.SetGroupVersionKind(planListGVK)
			if err := r.List(ctx, planList,
				client.InNamespace(obj.GetNamespace()),
				client.MatchingFields{fieldIndex: obj.GetName()},
				client.MatchingLabels{labelCreatedBy: labelCCLMValue},
			); err != nil {
				log.FromContext(ctx).Error(err, "failed to list Plans for map event",
					"fieldIndex", fieldIndex, "map", obj.GetName())
				return nil
			}
			reqs := make([]reconcile.Request, len(planList.Items))
			for i := range planList.Items {
				reqs[i] = reconcile.Request{NamespacedName: types.NamespacedName{
					Name:      planList.Items[i].GetName(),
					Namespace: planList.Items[i].GetNamespace(),
				}}
			}
			return reqs
		}
	}

	netMapType := newUnstructured(networkMapGVK)
	stgMapType := newUnstructured(storageMapGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(planType, builder.WithPredicates(cclmPred)).
		Watches(netMapType,
			handler.EnqueueRequestsFromMapFunc(mapToPlan(fieldIndexNetworkMap)),
			builder.WithPredicates(cclmPred)).
		Watches(stgMapType,
			handler.EnqueueRequestsFromMapFunc(mapToPlan(fieldIndexStorageMap)),
			builder.WithPredicates(cclmPred)).
		Complete(r)
}

func newUnstructured(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}
