package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

type ConfigMapReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
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

	if isManagedReplica(&src) {
		return ctrl.Result{}, nil
	}

	selector := listTargetSelector(&src)
	excludeSelector := listExcludeSelector(&src)
	matchingSel, err := listMatchingSelector(&src)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid %s annotation: %w", AnnotationReplicateToMatching, err)
	}
	desc := sourceDescriptor{
		kind:      "ConfigMap",
		namespace: src.Namespace,
		name:      src.Name,
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

	if selector.empty() && matchingSel == nil {
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

	targets, err := resolveTargetNamespaces(ctx, r.Client, selector, excludeSelector, matchingSel, src.Namespace)
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
			if target.Labels == nil {
				target.Labels = map[string]string{}
			}
			target.Labels[LabelManagedBy] = ManagedByValue
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

	if r.Recorder != nil {
		if len(errs) == 0 {
			r.Recorder.Eventf(&src, corev1.EventTypeNormal, "ReplicationSucceeded",
				"Replicated to %d namespace(s)", len(targets))
			ReplicationsTotal.WithLabelValues("ConfigMap", "success").Add(float64(len(targets)))
		} else {
			r.Recorder.Eventf(&src, corev1.EventTypeWarning, "ReplicationFailed",
				"Failed to replicate to %d/%d namespace(s)", len(errs), len(targets))
			ReplicationsTotal.WithLabelValues("ConfigMap", "error").Add(float64(len(errs)))
		}
	}

	return ctrl.Result{}, errors.Join(errs...)
}

func (r *ConfigMapReconciler) cleanupReplicatedConfigMaps(ctx context.Context, desc sourceDescriptor, keep map[string]struct{}) error {
	var all corev1.ConfigMapList
	if err := r.List(ctx, &all, client.MatchingLabels{LabelManagedBy: ManagedByValue}); err != nil {
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

func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
				ann := obj.GetAnnotations()
				if ann[AnnotationManagedBy] != ManagedByValue {
					return nil
				}
				ns, name, ok := parseSourceFrom("ConfigMap", ann[AnnotationSourceFrom])
				if !ok {
					return nil
				}
				return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
			}),
		).
		Complete(r)
}
