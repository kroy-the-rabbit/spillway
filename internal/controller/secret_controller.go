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

type SecretReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
	// SelfHealInterval requeues active source objects to recover from missed watch events.
	SelfHealInterval time.Duration
}

func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("secret", req.NamespacedName.String())

	var src corev1.Secret
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
		kind:      "Secret",
		namespace: src.Namespace,
		name:      src.Name,
	}

	if !src.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&src, FinalizerName) {
			if _, err := r.cleanupReplicatedSecrets(ctx, desc, nil); err != nil {
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
			if _, err := r.cleanupReplicatedSecrets(ctx, desc, nil); err != nil {
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
	skippedConflicts := 0
	var errs []error
	for _, ns := range targets {
		desired[ns] = struct{}{}

		target := &corev1.Secret{}
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
			target.Type = src.Type
			target.Immutable = ptrBool(src.Immutable)
			target.Data = copyByteMap(src.Data)
			return nil
		})
		if syncErr != nil {
			if isOwnershipConflict(syncErr) {
				log.Info("skipping secret sync to target namespace", "namespace", ns, "reason", syncErr.Error())
				skippedConflicts++
			} else {
				log.Error(syncErr, "failed to sync secret to target namespace", "namespace", ns)
				errs = append(errs, syncErr)
			}
			continue
		}
		if op != controllerutil.OperationResultNone {
			changedCount++
			if action := reconcileActionFromOperationResult(op); action != "" {
				ReconcileChangesTotal.WithLabelValues("Secret", action).Inc()
			}
		}
	}

	deletedCount, err := r.cleanupReplicatedSecrets(ctx, desc, desired)
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
				ReplicationsTotal.WithLabelValues("Secret", "success").Add(float64(changedCount))
			}
			if skippedConflicts > 0 {
				r.Recorder.Eventf(&src, corev1.EventTypeNormal, "ReplicationSkipped",
					"Skipped %d target namespace(s) with pre-existing unmanaged objects", skippedConflicts)
			}
		} else {
			r.Recorder.Eventf(&src, corev1.EventTypeWarning, "ReplicationFailed",
				"Failed to replicate to %d/%d namespace(s)", len(errs), len(targets))
			ReplicationsTotal.WithLabelValues("Secret", "error").Add(float64(len(errs)))
			if skippedConflicts > 0 {
				r.Recorder.Eventf(&src, corev1.EventTypeNormal, "ReplicationSkipped",
					"Skipped %d target namespace(s) with pre-existing unmanaged objects", skippedConflicts)
			}
		}
	}

	result := ctrl.Result{}
	if len(errs) == 0 && r.SelfHealInterval > 0 {
		result.RequeueAfter = r.SelfHealInterval
	}
	return result, errors.Join(errs...)
}

func (r *SecretReconciler) cleanupReplicatedSecrets(ctx context.Context, desc sourceDescriptor, keep map[string]struct{}) (int, error) {
	var all corev1.SecretList
	if err := r.List(
		ctx,
		&all,
		client.MatchingLabels{LabelManagedBy: ManagedByValue},
		client.MatchingFields{secretReplicaSourceFieldIdx: desc.sourceFrom()},
	); err != nil {
		return 0, err
	}

	deleted := 0
	for i := range all.Items {
		s := &all.Items[i]
		if !matchesSource(s, desc.kind, desc.namespace, desc.name) {
			continue
		}
		if keep != nil {
			if _, ok := keep[s.Namespace]; ok {
				continue
			}
		}
		if err := safeDelete(ctx, r.Client, s); err != nil {
			return deleted, err
		}
		deleted++
	}
	if deleted > 0 {
		CleanupDeletesTotal.WithLabelValues("Secret").Add(float64(deleted))
		ReconcileChangesTotal.WithLabelValues("Secret", "delete").Add(float64(deleted))
	}
	return deleted, nil
}

func (r *SecretReconciler) sourceRequestsForNamespace(ctx context.Context, obj client.Object) []ctrl.Request {
	ns, ok := obj.(*corev1.Namespace)
	if !ok || ns == nil {
		return nil
	}

	var all corev1.SecretList
	if err := r.List(ctx, &all); err != nil {
		r.Log.Error(err, "failed to list secrets for namespace event fan-out", "namespace", ns.Name)
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for i := range all.Items {
		src := &all.Items[i]
		if isManagedReplica(src) {
			continue
		}
		match, err := namespaceChangeAffectsSource(src, ns)
		if err != nil {
			r.Log.V(1).Info("skipping namespace fan-out for secret due to invalid selector", "secret", client.ObjectKeyFromObject(src).String(), "error", err.Error())
			continue
		}
		if !match {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(src)})
	}
	return reqs
}

func (r *SecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerSecretReplicaIndex(context.Background(), mgr); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(sourceObjectPredicate())).
		Watches(
			&corev1.Secret{},
			handler.Funcs{
				DeleteFunc: func(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
					enqueueReplicaDeleteSourceRemap(ctx, r.Log, q, "Secret", evt)
				},
			},
			builder.WithPredicates(managedReplicaDeleteOnlyPredicate()),
		).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.sourceRequestsForNamespace),
			builder.WithPredicates(namespaceEventPredicate()),
		).
		Complete(r)
}
