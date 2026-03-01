package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	spillwayv1alpha1 "spillway/api/v1alpha1"
)

// ProfileReconciler reconciles SpillwayProfile objects.
type ProfileReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Log              logr.Logger
	Recorder         record.EventRecorder
	SelfHealInterval time.Duration
}

func (r *ProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("profile", req.NamespacedName.String())

	var profile spillwayv1alpha1.SpillwayProfile
	if err := r.Get(ctx, req.NamespacedName, &profile); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	profileRef := fmt.Sprintf("%s/%s", profile.Namespace, profile.Name)

	// Handle deletion.
	if !profile.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&profile, FinalizerName) {
			if err := r.cleanupStaleProfileReplicas(ctx, profileRef, nil); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&profile, FinalizerName)
			if err := r.Update(ctx, &profile); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer.
	if ensureFinalizer(&profile) {
		if err := r.Update(ctx, &profile); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Build target selector from profile spec.
	include := buildTargetSelector(profile.Spec.TargetNamespaces)
	exclude := buildTargetSelector(profile.Spec.ExcludeNamespaces)

	var matchingSel labels.Selector
	if profile.Spec.TargetSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(profile.Spec.TargetSelector)
		if err != nil {
			log.Info("invalid targetSelector", "error", err.Error())
			r.Recorder.Eventf(&profile, corev1.EventTypeWarning, "InvalidSelector",
				"Invalid targetSelector: %v", err)
			return ctrl.Result{}, nil
		}
		matchingSel = sel
	}

	// Resolve target namespaces. Use empty sourceName for namespace-level consent only.
	targets, err := resolveTargetNamespaces(ctx, r.Client, include, exclude, matchingSel, profile.Namespace, "")
	if err != nil {
		return ctrl.Result{}, err
	}

	// desired tracks (namespace + "/" + kind + "/" + name) of all intended replicas.
	desired := map[string]struct{}{}
	changedCount := 0
	var errs []error

	for _, srcSpec := range profile.Spec.Sources {
		kf := keyFilterFromLists(srcSpec.IncludeKeys, srcSpec.ExcludeKeys)
		switch srcSpec.Kind {
		case "Secret":
			n, err := r.syncProfileSecret(ctx, log, &profile, profileRef, srcSpec.Name, kf, targets, desired)
			changedCount += n
			if err != nil {
				errs = append(errs, err)
			}
		case "ConfigMap":
			n, err := r.syncProfileConfigMap(ctx, log, &profile, profileRef, srcSpec.Name, kf, targets, desired)
			changedCount += n
			if err != nil {
				errs = append(errs, err)
			}
		default:
			log.Info("unknown source kind in profile, skipping", "kind", srcSpec.Kind, "name", srcSpec.Name)
		}
	}

	// Remove replicas that are no longer in the desired set.
	if cleanErr := r.cleanupStaleProfileReplicas(ctx, profileRef, desired); cleanErr != nil {
		errs = append(errs, cleanErr)
	}

	// Update status with the set of replicated namespaces.
	replicatedSet := map[string]struct{}{}
	for k := range desired {
		parts := strings.SplitN(k, "/", 2)
		replicatedSet[parts[0]] = struct{}{}
	}
	replicatedNS := make([]string, 0, len(replicatedSet))
	for ns := range replicatedSet {
		replicatedNS = append(replicatedNS, ns)
	}
	sort.Strings(replicatedNS)
	profile.Status.ReplicatedNamespaces = replicatedNS
	if sErr := r.Status().Update(ctx, &profile); sErr != nil && !apierrors.IsNotFound(sErr) {
		log.V(1).Info("failed to update profile status", "error", sErr)
	}

	if len(errs) == 0 && changedCount > 0 {
		r.Recorder.Eventf(&profile, corev1.EventTypeNormal, "ReplicationSucceeded",
			"Applied %d change(s) across %d target namespace(s)", changedCount, len(targets))
	} else if len(errs) > 0 {
		r.Recorder.Eventf(&profile, corev1.EventTypeWarning, "ReplicationFailed",
			"Encountered %d error(s) during replication", len(errs))
	}

	result := ctrl.Result{}
	if len(errs) == 0 && r.SelfHealInterval > 0 {
		result.RequeueAfter = r.SelfHealInterval
	}
	return result, errors.Join(errs...)
}

func (r *ProfileReconciler) syncProfileSecret(
	ctx context.Context,
	log logr.Logger,
	profile *spillwayv1alpha1.SpillwayProfile,
	profileRef, srcName string,
	kf keyFilter,
	targets []string,
	desired map[string]struct{},
) (int, error) {
	var src corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: profile.Namespace, Name: srcName}, &src); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("source secret not found, skipping", "name", srcName)
			return 0, nil
		}
		return 0, err
	}

	changed := 0
	for _, ns := range targets {
		key := ns + "/Secret/" + srcName
		desired[key] = struct{}{}

		target := &corev1.Secret{}
		target.Name = srcName
		target.Namespace = ns
		op, syncErr := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
			// Refuse to adopt replicas owned by a different profile.
			if target.UID != "" {
				existingRef := target.Annotations[AnnotationProfileRef]
				if existingRef != "" && existingRef != profileRef {
					return ownershipConflictError{
						objType: "Secret", namespace: ns, name: srcName,
						sourceKey: profileRef,
					}
				}
			}
			if target.Labels == nil {
				target.Labels = map[string]string{}
			}
			target.Labels[LabelManagedBy] = ManagedByValue
			target.Annotations = map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationProfileRef: profileRef,
			}
			target.Type = src.Type
			target.Immutable = ptrBool(src.Immutable)
			target.Data = kf.applyBytes(src.Data)
			return nil
		})
		if syncErr != nil {
			if isOwnershipConflict(syncErr) {
				log.Info("skipping profile secret: owned by another profile",
					"namespace", ns, "name", srcName)
			} else {
				log.Error(syncErr, "failed to sync profile secret", "namespace", ns, "name", srcName)
				return changed, syncErr
			}
			continue
		}
		if op != controllerutil.OperationResultNone {
			changed++
			if action := reconcileActionFromOperationResult(op); action != "" {
				ReconcileChangesTotal.WithLabelValues("Secret", action).Inc()
			}
		}
	}
	return changed, nil
}

func (r *ProfileReconciler) syncProfileConfigMap(
	ctx context.Context,
	log logr.Logger,
	profile *spillwayv1alpha1.SpillwayProfile,
	profileRef, srcName string,
	kf keyFilter,
	targets []string,
	desired map[string]struct{},
) (int, error) {
	var src corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: profile.Namespace, Name: srcName}, &src); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("source configmap not found, skipping", "name", srcName)
			return 0, nil
		}
		return 0, err
	}

	changed := 0
	for _, ns := range targets {
		key := ns + "/ConfigMap/" + srcName
		desired[key] = struct{}{}

		target := &corev1.ConfigMap{}
		target.Name = srcName
		target.Namespace = ns
		op, syncErr := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
			if target.UID != "" {
				existingRef := target.Annotations[AnnotationProfileRef]
				if existingRef != "" && existingRef != profileRef {
					return ownershipConflictError{
						objType: "ConfigMap", namespace: ns, name: srcName,
						sourceKey: profileRef,
					}
				}
			}
			if target.Labels == nil {
				target.Labels = map[string]string{}
			}
			target.Labels[LabelManagedBy] = ManagedByValue
			target.Annotations = map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationProfileRef: profileRef,
			}
			target.Immutable = ptrBool(src.Immutable)
			target.Data = kf.applyString(src.Data)
			target.BinaryData = kf.applyBytes(src.BinaryData)
			return nil
		})
		if syncErr != nil {
			if isOwnershipConflict(syncErr) {
				log.Info("skipping profile configmap: owned by another profile",
					"namespace", ns, "name", srcName)
			} else {
				log.Error(syncErr, "failed to sync profile configmap", "namespace", ns, "name", srcName)
				return changed, syncErr
			}
			continue
		}
		if op != controllerutil.OperationResultNone {
			changed++
			if action := reconcileActionFromOperationResult(op); action != "" {
				ReconcileChangesTotal.WithLabelValues("ConfigMap", action).Inc()
			}
		}
	}
	return changed, nil
}

// cleanupStaleProfileReplicas deletes profile-owned replicas not present in desired.
// Pass nil desired to delete all replicas (used during profile deletion).
func (r *ProfileReconciler) cleanupStaleProfileReplicas(ctx context.Context, profileRef string, desired map[string]struct{}) error {
	var secretList corev1.SecretList
	if err := r.List(ctx, &secretList, client.MatchingFields{secretProfileRefFieldIdx: profileRef}); err != nil {
		return err
	}
	for i := range secretList.Items {
		s := &secretList.Items[i]
		if s.Annotations[AnnotationProfileRef] != profileRef {
			continue
		}
		if desired != nil {
			if _, ok := desired[s.Namespace+"/Secret/"+s.Name]; ok {
				continue
			}
		}
		if err := safeDelete(ctx, r.Client, s); err != nil {
			return err
		}
		CleanupDeletesTotal.WithLabelValues("Secret").Inc()
	}

	var cmList corev1.ConfigMapList
	if err := r.List(ctx, &cmList, client.MatchingFields{configMapProfileRefFieldIdx: profileRef}); err != nil {
		return err
	}
	for i := range cmList.Items {
		cm := &cmList.Items[i]
		if cm.Annotations[AnnotationProfileRef] != profileRef {
			continue
		}
		if desired != nil {
			if _, ok := desired[cm.Namespace+"/ConfigMap/"+cm.Name]; ok {
				continue
			}
		}
		if err := safeDelete(ctx, r.Client, cm); err != nil {
			return err
		}
		CleanupDeletesTotal.WithLabelValues("ConfigMap").Inc()
	}
	return nil
}

// sourceRequestsForNamespace fans out namespace create/update events to all profiles.
func (r *ProfileReconciler) sourceRequestsForNamespace(ctx context.Context, obj client.Object) []ctrl.Request {
	if _, ok := obj.(*corev1.Namespace); !ok {
		return nil
	}
	var profiles spillwayv1alpha1.SpillwayProfileList
	if err := r.List(ctx, &profiles); err != nil {
		r.Log.Error(err, "failed to list profiles for namespace event")
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(profiles.Items))
	for i := range profiles.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&profiles.Items[i])})
	}
	return reqs
}

func (r *ProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerSecretProfileRefIndex(context.Background(), mgr); err != nil {
		return err
	}
	if err := registerConfigMapProfileRefIndex(context.Background(), mgr); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&spillwayv1alpha1.SpillwayProfile{}).
		Watches(
			&corev1.Secret{},
			handler.Funcs{
				DeleteFunc: func(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
					enqueueProfileFromReplicaDelete(ctx, r.Log, q, evt)
				},
			},
			builder.WithPredicates(profileReplicaDeleteOnlyPredicate()),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.Funcs{
				DeleteFunc: func(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
					enqueueProfileFromReplicaDelete(ctx, r.Log, q, evt)
				},
			},
			builder.WithPredicates(profileReplicaDeleteOnlyPredicate()),
		).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.sourceRequestsForNamespace),
			builder.WithPredicates(namespaceEventPredicate()),
		).
		Complete(r)
}

// buildTargetSelector converts a slice of namespace names/globs into a targetSelector.
func buildTargetSelector(names []string) targetSelector {
	sel := targetSelector{exact: map[string]struct{}{}}
	for _, ns := range names {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		switch {
		case ns == "all" || ns == "*":
			sel.all = true
		case strings.Contains(ns, "*"):
			sel.globs = append(sel.globs, ns)
		default:
			sel.exact[ns] = struct{}{}
		}
	}
	sort.Strings(sel.globs)
	return sel
}
