package controller

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ConfigMapReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("configmap", req.NamespacedName.String())

	var src corev1.ConfigMap
	if err := r.Get(ctx, req.NamespacedName, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if isManagedReplica(&src) || isSkipped(&src) {
		return ctrl.Result{}, nil
	}

	selector := listTargetSelector(&src)
	excludeSelector := listExcludeSelector(&src)
	desc := sourceDescriptor{
		kind:      "ConfigMap",
		namespace: src.Namespace,
		name:      src.Name,
	}

	// Fall back to the namespace-level default selector when the object has no
	// per-object annotation.
	if selector.empty() {
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: src.Namespace}, &ns); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		} else if err == nil {
			selector = nsDefaultSelector(ns)
		}
	}

	if !src.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&src, FinalizerName) {
			if err := r.cleanupReplicatedConfigMaps(ctx, desc, nil); err != nil {
				return ctrl.Result{}, err
			}
			removeFinalizer(&src)
			if err := r.Update(ctx, &src); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if selector.empty() {
		if controllerutil.ContainsFinalizer(&src, FinalizerName) {
			if err := r.cleanupReplicatedConfigMaps(ctx, desc, nil); err != nil {
				return ctrl.Result{}, err
			}
			removeFinalizer(&src)
			if err := r.Update(ctx, &src); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if ensureFinalizer(&src) {
		if err := r.Update(ctx, &src); err != nil {
			return ctrl.Result{}, err
		}
	}

	targets, err := resolveTargetNamespaces(ctx, r.Client, selector, excludeSelector, src.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	logTargetSync(log, desc, targets)

	forceAdopt := isForceAdopt(&src)
	desired := map[string]struct{}{}
	var errs []error
	for _, ns := range targets {
		desired[ns] = struct{}{}

		target := &corev1.ConfigMap{}
		target.Name = src.Name
		target.Namespace = ns
		_, syncErr := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
			if err := ensureManagedOwnership(target, desc, forceAdopt); err != nil {
				return err
			}
			target.Labels = copyStringMap(src.Labels)
			target.Annotations = desc.targetAnnotations(src.Annotations)
			target.Immutable = ptrBool(src.Immutable)
			target.Data = copyStringMap(src.Data)
			target.BinaryData = copyByteMap(src.BinaryData)
			return nil
		})
		if syncErr != nil {
			log.Error(syncErr, "failed to sync configmap to target namespace", "namespace", ns)
			errs = append(errs, syncErr)
		}
	}

	if err := r.cleanupReplicatedConfigMaps(ctx, desc, desired); err != nil {
		errs = append(errs, err)
	}

	return ctrl.Result{}, errors.Join(errs...)
}

func (r *ConfigMapReconciler) cleanupReplicatedConfigMaps(ctx context.Context, desc sourceDescriptor, keep map[string]struct{}) error {
	var all corev1.ConfigMapList
	if err := r.List(ctx, &all); err != nil {
		return err
	}

	for i := range all.Items {
		cm := &all.Items[i]
		if !matchesSource(cm, desc.kind, desc.namespace, desc.name) {
			continue
		}
		if keep != nil {
			if _, ok := keep[cm.Namespace]; ok {
				continue
			}
		}
		if err := safeDelete(ctx, r.Client, cm); err != nil {
			return err
		}
	}
	return nil
}

// namespacesToConfigMaps maps a Namespace event to reconcile requests for every
// ConfigMap in that namespace. Used to react to namespace label changes.
func (r *ConfigMapReconciler) namespacesToConfigMaps(ctx context.Context, obj client.Object) []reconcile.Request {
	var list corev1.ConfigMapList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetName())); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      list.Items[i].Name,
				Namespace: list.Items[i].Namespace,
			},
		})
	}
	return reqs
}

func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.namespacesToConfigMaps),
			builder.WithPredicates(predicate.LabelChangedPredicate{}),
		).
		Complete(r)
}
