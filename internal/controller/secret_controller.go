package controller

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// OrphanAuditInterval sets how often to scan for and delete orphaned replicas
	// whose source no longer exists. Zero (default) disables the audit.
	OrphanAuditInterval time.Duration

	auditMu   sync.Mutex
	lastAudit time.Time
}

// secretReconcileConfig returns the type-specific reconcile configuration for
// the SecretReconciler, used by the shared reconcileObject function.
func secretReconcileConfig() reconcileConfig[*corev1.Secret] {
	return reconcileConfig[*corev1.Secret]{
		kind:                  "Secret",
		replicaSourceFieldIdx: secretReplicaSourceFieldIdx,
		applyData: func(src, target *corev1.Secret, kf keyFilter) {
			target.Type = src.Type
			target.Immutable = ptrBool(src.Immutable)
			target.Data = kf.applyBytes(src.Data)
		},
		newObject: func() *corev1.Secret { return &corev1.Secret{} },
		listReplicas: func(ctx context.Context, c client.Client, fieldIdx, sourceFrom string) ([]*corev1.Secret, error) {
			var all corev1.SecretList
			if err := c.List(ctx, &all, client.MatchingFields{fieldIdx: sourceFrom}); err != nil {
				return nil, err
			}
			out := make([]*corev1.Secret, len(all.Items))
			for i := range all.Items {
				out[i] = &all.Items[i]
			}
			return out, nil
		},
	}
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

	result, err := reconcileObject(ctx, r.Client, log, r.Recorder, r.SelfHealInterval, secretReconcileConfig(), &src)
	if err != nil {
		return result, err
	}

	// Run orphan audit if interval has elapsed.
	if r.OrphanAuditInterval > 0 {
		r.auditMu.Lock()
		due := time.Since(r.lastAudit) >= r.OrphanAuditInterval
		if due {
			r.lastAudit = time.Now()
		}
		r.auditMu.Unlock()
		if due {
			if auditErr := r.auditOrphanedSecretReplicas(ctx); auditErr != nil {
				r.Log.Error(auditErr, "orphan audit failed")
			}
		}
	}

	return result, nil
}

// auditOrphanedSecretReplicas scans all managed Secret replicas and deletes
// any whose source Secret no longer exists. Profile replicas are skipped.
func (r *SecretReconciler) auditOrphanedSecretReplicas(ctx context.Context) error {
	var all corev1.SecretList
	if err := r.List(ctx, &all, client.MatchingLabels{LabelManagedBy: ManagedByValue}); err != nil {
		return err
	}
	for i := range all.Items {
		replica := &all.Items[i]
		// Skip profile-managed replicas.
		if isProfileReplica(replica) {
			continue
		}
		sourceFrom := replica.Annotations[AnnotationSourceFrom]
		ns, name, ok := parseSourceFrom("Secret", sourceFrom)
		if !ok {
			continue
		}
		var src corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &src); err != nil {
			if apierrors.IsNotFound(err) {
				r.Log.Info("deleting orphaned Secret replica: source no longer exists",
					"replica", client.ObjectKeyFromObject(replica).String(),
					"source", sourceFrom)
				if delErr := safeDelete(ctx, r.Client, replica); delErr != nil {
					r.Log.Error(delErr, "failed to delete orphaned Secret replica")
				}
			}
		}
	}
	return nil
}

func (r *SecretReconciler) sourceRequestsForNamespace(ctx context.Context, obj client.Object) []ctrl.Request {
	ns, ok := obj.(*corev1.Namespace)
	if !ok || ns == nil {
		return nil
	}

	var all corev1.SecretList
	if err := r.List(ctx, &all, client.MatchingFields{secretSourceTargetingFieldIdx: "true"}); err != nil {
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
	if err := registerSecretSourceTargetingIndex(context.Background(), mgr); err != nil {
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
