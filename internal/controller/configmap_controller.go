package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

type ConfigMapReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
	// SelfHealInterval requeues active source objects to recover from missed watch events.
	SelfHealInterval time.Duration
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
			if _, err := r.cleanupReplicatedConfigMaps(ctx, desc, nil); err != nil {
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
			if _, err := r.cleanupReplicatedConfigMaps(ctx, desc, nil); err != nil {
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
	changedCount := 0
	var errs []error
	for _, ns := range targets {
		desired[ns] = struct{}{}

		target := &corev1.ConfigMap{}
		target.Name = src.Name
		target.Namespace = ns
		op, syncErr := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
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
			continue
		}
		if op != controllerutil.OperationResultNone {
			changedCount++
			if action := reconcileActionFromOperationResult(op); action != "" {
				ReconcileChangesTotal.WithLabelValues("ConfigMap", action).Inc()
			}
		}
	}

	deletedCount, err := r.cleanupReplicatedConfigMaps(ctx, desc, desired)
	if err != nil {
		errs = append(errs, err)
	} else {
		changedCount += deletedCount
	}

	if r.Recorder != nil {
		if len(errs) == 0 {
			if changedCount > 0 {
				r.Recorder.Eventf(&src, corev1.EventTypeNormal, "ReplicationSucceeded",
					"Applied %d change(s) across %d target namespace(s)", changedCount, len(targets))
				ReplicationsTotal.WithLabelValues("ConfigMap", "success").Add(float64(changedCount))
			}
		} else {
			r.Recorder.Eventf(&src, corev1.EventTypeWarning, "ReplicationFailed",
				"Failed to replicate to %d/%d namespace(s)", len(errs), len(targets))
			ReplicationsTotal.WithLabelValues("ConfigMap", "error").Add(float64(len(errs)))
		}
	}

	result := ctrl.Result{}
	if len(errs) == 0 && r.SelfHealInterval > 0 {
		result.RequeueAfter = r.SelfHealInterval
	}
	return result, errors.Join(errs...)
}

func (r *ConfigMapReconciler) cleanupReplicatedConfigMaps(ctx context.Context, desc sourceDescriptor, keep map[string]struct{}) (int, error) {
	var all corev1.ConfigMapList
	if err := r.List(
		ctx,
		&all,
		client.MatchingLabels{LabelManagedBy: ManagedByValue},
		client.MatchingFields{configMapReplicaSourceFieldIdx: desc.sourceFrom()},
	); err != nil {
		return 0, err
	}

	deleted := 0
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
			return deleted, err
		}
		deleted++
	}
	if deleted > 0 {
		CleanupDeletesTotal.WithLabelValues("ConfigMap").Add(float64(deleted))
		ReconcileChangesTotal.WithLabelValues("ConfigMap", "delete").Add(float64(deleted))
	}
	return deleted, nil
}

func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerConfigMapReplicaIndex(context.Background(), mgr); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(sourceObjectPredicate())).
		Watches(
			&corev1.ConfigMap{},
			handler.Funcs{
				DeleteFunc: func(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
					enqueueReplicaDeleteSourceRemap(ctx, r.Log, q, "ConfigMap", evt)
				},
			},
			builder.WithPredicates(managedReplicaDeleteOnlyPredicate()),
		).
		Complete(r)
}
