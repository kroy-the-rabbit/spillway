package controller

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileConfig holds the type-specific callbacks and metadata needed to
// run the shared replication reconcile loop for either Secrets or ConfigMaps.
//
// Both SecretReconciler and ConfigMapReconciler embed this config and delegate
// their Reconcile method to reconcileObject.
type reconcileConfig[T client.Object] struct {
	// kind is "Secret" or "ConfigMap" — used for logging and metrics.
	kind string

	// replicaSourceFieldIdx is the field index name used to list replicas by source.
	replicaSourceFieldIdx string

	// applyData copies type-specific data fields from src into target.
	// It is called inside the CreateOrUpdate mutate function.
	applyData func(src, target T, kf keyFilter)

	// newObject returns a new empty object of type T.
	newObject func() T

	// listReplicas lists all replicas for a given source descriptor.
	listReplicas func(ctx context.Context, c client.Client, fieldIdx, sourceFrom string) ([]T, error)
}

// reconcileObject contains the shared reconcile logic used by both
// SecretReconciler and ConfigMapReconciler. It is parameterised on the object
// type T (corev1.Secret or corev1.ConfigMap).
func reconcileObject[T client.Object](
	ctx context.Context,
	c client.Client,
	log logr.Logger,
	recorder record.EventRecorder,
	selfHealInterval time.Duration,
	cfg reconcileConfig[T],
	src T,
) (ctrl.Result, error) {
	selector := listTargetSelector(src)
	excludeSelector := listExcludeSelector(src)
	matchingSel, err := listMatchingSelector(src)
	if err != nil {
		log.Info("invalid replicate-to-matching selector; skipping until annotation is fixed", "error", err.Error())
		if recorder != nil {
			recorder.Eventf(src, corev1.EventTypeWarning, "InvalidSelector",
				"Invalid %s annotation: %v", AnnotationReplicateToMatching, err)
		}
		return ctrl.Result{}, nil
	}
	desc := sourceDescriptor{
		kind:      cfg.kind,
		namespace: src.GetNamespace(),
		name:      src.GetName(),
	}

	if !src.GetDeletionTimestamp().IsZero() {
		if controllerutil.ContainsFinalizer(src, FinalizerName) {
			if _, err := cleanupReplicas(ctx, c, cfg, desc, nil); err != nil {
				return ctrl.Result{}, err
			}
			removeFinalizer(src)
			if err := c.Update(ctx, src); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if selector.empty() && matchingSel == nil {
		if controllerutil.ContainsFinalizer(src, FinalizerName) {
			if _, err := cleanupReplicas(ctx, c, cfg, desc, nil); err != nil {
				return ctrl.Result{}, err
			}
			removeFinalizer(src)
			if err := c.Update(ctx, src); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if ensureFinalizer(src) {
		if err := c.Update(ctx, src); err != nil {
			return ctrl.Result{}, err
		}
	}

	targets, err := resolveTargetNamespaces(ctx, c, selector, excludeSelector, matchingSel, cfg.kind, src.GetNamespace(), src.GetName())
	if err != nil {
		return ctrl.Result{}, err
	}
	logTargetSync(log, desc, targets)

	forceAdopt := isForceAdopt(src)
	kf := parseKeyFilter(src)
	ttl, hasTTL := parseReplicaTTL(src)

	// If TTL was removed, clear any previously recorded expired namespaces.
	if !hasTTL && src.GetAnnotations()[AnnotationExpiredNamespaces] != "" {
		ann := copyStringMap(src.GetAnnotations())
		delete(ann, AnnotationExpiredNamespaces)
		src.SetAnnotations(ann)
		if err := c.Update(ctx, src); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	expiredNS := parseExpiredNamespaces(src)
	var newlyExpired []string

	desired := map[string]struct{}{}
	changedCount := 0
	skippedConflicts := 0
	var errs []error

	for _, ns := range targets {
		// Skip namespaces where the TTL has already permanently expired.
		if hasTTL {
			if _, ok := expiredNS[ns]; ok {
				continue
			}
			// Check if an existing replica has just expired.
			existing := cfg.newObject()
			existing.SetName(src.GetName())
			existing.SetNamespace(ns)
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: src.GetName()}, existing); err == nil {
				if replicaIsExpired(existing) {
					newlyExpired = append(newlyExpired, ns)
					continue // leave out of desired; cleanup will delete it
				}
			}
		}

		desired[ns] = struct{}{}

		target := cfg.newObject()
		target.SetName(src.GetName())
		target.SetNamespace(ns)
		op, syncErr := controllerutil.CreateOrUpdate(ctx, c, target, func() error {
			if err := ensureManagedOwnership(target, desc, forceAdopt); err != nil {
				return err
			}
			lbls := copyStringMap(src.GetLabels())
			if lbls == nil {
				lbls = map[string]string{}
			}
			lbls[LabelManagedBy] = ManagedByValue
			target.SetLabels(lbls)
			// Preserve existing expires-at before overwriting annotations.
			existingExpiry := target.GetAnnotations()[AnnotationExpiresAt]
			target.SetAnnotations(desc.targetAnnotations(src.GetAnnotations()))
			cfg.applyData(src, target, kf)
			// Stamp or preserve TTL expiry.
			if hasTTL {
				ann := target.GetAnnotations()
				if existingExpiry != "" {
					ann[AnnotationExpiresAt] = existingExpiry
				} else {
					ann[AnnotationExpiresAt] = time.Now().Add(ttl).Format(time.RFC3339)
				}
				target.SetAnnotations(ann)
			}
			return nil
		})
		if syncErr != nil {
			if isOwnershipConflict(syncErr) {
				log.Info("skipping "+cfg.kind+" sync to target namespace", "namespace", ns, "reason", syncErr.Error())
				skippedConflicts++
			} else {
				log.Error(syncErr, "failed to sync "+cfg.kind+" to target namespace", "namespace", ns)
				errs = append(errs, syncErr)
			}
			continue
		}
		if op != controllerutil.OperationResultNone {
			changedCount++
			if action := reconcileActionFromOperationResult(op); action != "" {
				ReconcileChangesTotal.WithLabelValues(cfg.kind, action).Inc()
			}
		}
	}

	deletedCount, err := cleanupReplicas(ctx, c, cfg, desc, desired)
	if err != nil {
		errs = append(errs, err)
	} else {
		changedCount += deletedCount
	}

	// Prune expiredNS of namespaces that no longer exist in the cluster.
	var prunedExpiredNS bool
	for ns := range expiredNS {
		var nsObj corev1.Namespace
		if err := c.Get(ctx, client.ObjectKey{Name: ns}, &nsObj); apierrors.IsNotFound(err) {
			delete(expiredNS, ns)
			prunedExpiredNS = true
		}
	}

	// Persist newly expired namespaces on the source so they are skipped on
	// future reconciles and not recreated.
	if len(newlyExpired) > 0 || prunedExpiredNS {
		for _, ns := range newlyExpired {
			expiredNS[ns] = struct{}{}
		}
		ann := copyStringMap(src.GetAnnotations())
		if len(expiredNS) == 0 {
			delete(ann, AnnotationExpiredNamespaces)
		} else {
			ann[AnnotationExpiredNamespaces] = formatNamespaceSet(expiredNS)
		}
		src.SetAnnotations(ann)
		if updateErr := c.Update(ctx, src); updateErr != nil {
			errs = append(errs, updateErr)
		}
	}

	if len(errs) == 0 {
		if changedCount > 0 {
			recorder.Eventf(src, corev1.EventTypeNormal, "ReplicationSucceeded",
				"Applied %d change(s) across %d target namespace(s)", changedCount, len(targets))
			ReplicationsTotal.WithLabelValues(cfg.kind, "success").Add(float64(changedCount))
		}
		if skippedConflicts > 0 {
			recorder.Eventf(src, corev1.EventTypeNormal, "ReplicationSkipped",
				"Skipped %d target namespace(s) with pre-existing unmanaged objects", skippedConflicts)
		}
	} else {
		recorder.Eventf(src, corev1.EventTypeWarning, "ReplicationFailed",
			"Failed to replicate to %d/%d namespace(s)", len(errs), len(targets))
		ReplicationsTotal.WithLabelValues(cfg.kind, "error").Add(float64(len(errs)))
		if skippedConflicts > 0 {
			recorder.Eventf(src, corev1.EventTypeNormal, "ReplicationSkipped",
				"Skipped %d target namespace(s) with pre-existing unmanaged objects", skippedConflicts)
		}
	}

	result := ctrl.Result{}
	if len(errs) == 0 && selfHealInterval > 0 {
		result.RequeueAfter = selfHealInterval
	}
	return result, errors.Join(errs...)
}

// cleanupReplicas deletes all replicas for a given source that are not in the
// desired set. Pass nil desired to delete all replicas (for finalizer cleanup).
func cleanupReplicas[T client.Object](
	ctx context.Context,
	c client.Client,
	cfg reconcileConfig[T],
	desc sourceDescriptor,
	keep map[string]struct{},
) (int, error) {
	items, err := cfg.listReplicas(ctx, c, cfg.replicaSourceFieldIdx, desc.sourceFrom())
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, obj := range items {
		if !matchesSource(obj, desc.kind, desc.namespace, desc.name) {
			continue
		}
		if keep != nil {
			if _, ok := keep[obj.GetNamespace()]; ok {
				continue
			}
		}
		if err := safeDelete(ctx, c, obj); err != nil {
			return deleted, err
		}
		deleted++
	}
	if deleted > 0 {
		CleanupDeletesTotal.WithLabelValues(cfg.kind).Add(float64(deleted))
		ReconcileChangesTotal.WithLabelValues(cfg.kind, "delete").Add(float64(deleted))
	}
	return deleted, nil
}
