package controller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	AnnotationReplicateTo         = "spillway.kroy.io/replicate-to"
	AnnotationReplicateToMatching = "spillway.kroy.io/replicate-to-matching"
	AnnotationExcludeNS           = "spillway.kroy.io/exclude-namespaces"

	// AnnotationForceAdopt on a source Secret or ConfigMap tells spillway to
	// overwrite pre-existing objects in target namespaces even if they were not
	// previously managed by spillway.
	AnnotationForceAdopt = "spillway.kroy.io/force-adopt"

	// AnnotationIncludeKeys whitelists which data keys are copied into replicas.
	// Mutually exclusive with AnnotationExcludeKeys; include takes precedence.
	AnnotationIncludeKeys = "spillway.kroy.io/include-keys"

	// AnnotationExcludeKeys blacklists specific data keys from replicas.
	AnnotationExcludeKeys = "spillway.kroy.io/exclude-keys"

	// AnnotationReplicaTTL sets a lifetime for replicas (Go duration, e.g. "24h").
	// Replicas are deleted once the TTL expires from their creation time and
	// are NOT automatically recreated.
	AnnotationReplicaTTL = "spillway.kroy.io/replica-ttl"

	// AnnotationExpiresAt is stamped on replicas at creation time when the source
	// carries AnnotationReplicaTTL. RFC3339 timestamp.
	AnnotationExpiresAt = "spillway.kroy.io/expires-at"

	AnnotationManagedBy  = "spillway.kroy.io/managed-by"
	AnnotationSourceFrom = "spillway.kroy.io/source-from"

	// AnnotationProfileRef is set on replicas created by a SpillwayProfile.
	// Value is "<profile-namespace>/<profile-name>". Used for profile-scoped
	// cleanup and to distinguish profile replicas from annotation-based ones.
	AnnotationProfileRef = "spillway.kroy.io/profile-ref"

	// LabelManagedBy is the same key as AnnotationManagedBy, set as a label
	// on replica objects so cleanup can use a label-filtered List.
	LabelManagedBy = "spillway.kroy.io/managed-by"

	ManagedByValue = "spillway"
	FinalizerName  = "spillway.kroy.io/finalizer"

	// AnnotationAcceptFrom on a Namespace opts it in to receiving replicas
	// only from whitelisted sources. Absent = accept all (backward compatible).
	// Values: comma-separated "namespace" or "namespace:objectname" entries.
	// Use "all" or "*" to explicitly accept from everywhere.
	AnnotationAcceptFrom = "spillway.kroy.io/accept-from"

	secretReplicaSourceFieldIdx    = "spillway.kroy.io/index.secret-source-from"
	configMapReplicaSourceFieldIdx = "spillway.kroy.io/index.configmap-source-from"
	secretSourceTargetingFieldIdx  = "spillway.kroy.io/index.secret-has-targeting"
	cmSourceTargetingFieldIdx      = "spillway.kroy.io/index.configmap-has-targeting"
	secretProfileRefFieldIdx       = "spillway.kroy.io/index.secret-profile-ref"
	configMapProfileRefFieldIdx    = "spillway.kroy.io/index.configmap-profile-ref"
)

var defaultProtectedNamespaces = map[string]struct{}{
	"kube-system": {},
}

// ---------------------------------------------------------------------------
// Target selector
// ---------------------------------------------------------------------------

type targetSelector struct {
	all   bool
	exact map[string]struct{}
	globs []string
}

func parseTargetSelector(raw string) targetSelector {
	sel := targetSelector{exact: map[string]struct{}{}}
	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		switch {
		case token == "all" || token == "*":
			sel.all = true
		case strings.Contains(token, "*"):
			sel.globs = append(sel.globs, token)
		default:
			sel.exact[token] = struct{}{}
		}
	}
	sort.Strings(sel.globs)
	return sel
}

func (s targetSelector) empty() bool {
	return !s.all && len(s.exact) == 0 && len(s.globs) == 0
}

func (s targetSelector) matchesNamespace(name string) bool {
	if s.all {
		return true
	}
	if _, ok := s.exact[name]; ok {
		return true
	}
	for _, pattern := range s.globs {
		ok, err := path.Match(pattern, name)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func (s targetSelector) hasExact(name string) bool {
	_, ok := s.exact[name]
	return ok
}

// ---------------------------------------------------------------------------
// Key filter (include-keys / exclude-keys)
// ---------------------------------------------------------------------------

// keyFilter projects source data onto replicas. An empty filter copies all keys.
type keyFilter struct {
	include map[string]struct{} // non-nil → whitelist
	exclude map[string]struct{} // non-nil → blacklist (ignored when include set)
}

func (f keyFilter) empty() bool {
	return len(f.include) == 0 && len(f.exclude) == 0
}

func (f keyFilter) applyString(in map[string]string) map[string]string {
	if f.empty() {
		return copyStringMap(in)
	}
	out := make(map[string]string)
	for k, v := range in {
		if len(f.include) > 0 {
			if _, ok := f.include[k]; !ok {
				continue
			}
		} else if _, ok := f.exclude[k]; ok {
			continue
		}
		out[k] = v
	}
	return out
}

func (f keyFilter) applyBytes(in map[string][]byte) map[string][]byte {
	if f.empty() {
		return copyByteMap(in)
	}
	out := make(map[string][]byte)
	for k, v := range in {
		if len(f.include) > 0 {
			if _, ok := f.include[k]; !ok {
				continue
			}
		} else if _, ok := f.exclude[k]; ok {
			continue
		}
		if v == nil {
			out[k] = nil
			continue
		}
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func parseKeyFilter(obj metav1.Object) keyFilter {
	ann := obj.GetAnnotations()
	if raw := ann[AnnotationIncludeKeys]; raw != "" {
		return keyFilter{include: parseKeySet(raw)}
	}
	if raw := ann[AnnotationExcludeKeys]; raw != "" {
		return keyFilter{exclude: parseKeySet(raw)}
	}
	return keyFilter{}
}

func keyFilterFromLists(include, exclude []string) keyFilter {
	if len(include) > 0 {
		m := make(map[string]struct{}, len(include))
		for _, k := range include {
			if k = strings.TrimSpace(k); k != "" {
				m[k] = struct{}{}
			}
		}
		return keyFilter{include: m}
	}
	if len(exclude) > 0 {
		m := make(map[string]struct{}, len(exclude))
		for _, k := range exclude {
			if k = strings.TrimSpace(k); k != "" {
				m[k] = struct{}{}
			}
		}
		return keyFilter{exclude: m}
	}
	return keyFilter{}
}

func parseKeySet(raw string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			m[k] = struct{}{}
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Replica TTL
// ---------------------------------------------------------------------------

// parseReplicaTTL reads the replica-ttl annotation from a source object.
func parseReplicaTTL(obj metav1.Object) (time.Duration, bool) {
	raw := obj.GetAnnotations()[AnnotationReplicaTTL]
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// replicaIsExpired returns true if the replica's expires-at annotation is in the past.
func replicaIsExpired(obj metav1.Object) bool {
	raw := obj.GetAnnotations()[AnnotationExpiresAt]
	if raw == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return time.Now().After(t)
}

// ---------------------------------------------------------------------------
// Namespace consent (accept-from)
// ---------------------------------------------------------------------------

// checkNamespaceConsent returns true if the target namespace consents to
// receiving replicas from the given source namespace/name.
//
// Rules:
//   - Absent annotation → accept all (backward compatible default).
//   - "all" or "*" → accept all.
//   - "platform" → accept any object from the platform namespace.
//   - "platform:token" → accept only the object named "token" from platform.
//
// When srcName is empty the check is namespace-level only (object-specific
// tokens are skipped).
func checkNamespaceConsent(ns *corev1.Namespace, srcNamespace, srcName string) bool {
	if ns == nil {
		return true
	}
	raw := ns.GetAnnotations()[AnnotationAcceptFrom]
	if raw == "" {
		return true
	}
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		switch {
		case token == "" || token == "all" || token == "*":
			return true
		case strings.Contains(token, ":"):
			parts := strings.SplitN(token, ":", 2)
			if parts[0] != srcNamespace {
				continue
			}
			if srcName == "" {
				// Namespace-level check only — skip object-specific tokens.
				continue
			}
			if parts[1] == "*" || parts[1] == srcName {
				return true
			}
		default:
			if token == srcNamespace {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Namespace resolution
// ---------------------------------------------------------------------------

func resolveTargetNamespaces(
	ctx context.Context,
	c client.Client,
	include targetSelector,
	exclude targetSelector,
	matchingSel labels.Selector,
	sourceNamespace string,
	sourceName string,
) ([]string, error) {
	if include.empty() && matchingSel == nil {
		return nil, nil
	}

	// Fast path: explicit namespace targets only (no globs/all/label selector).
	if !include.all && len(include.globs) == 0 && matchingSel == nil {
		out := make([]string, 0, len(include.exact))
		for nsName := range include.exact {
			if nsName == sourceNamespace {
				continue
			}
			if exclude.matchesNamespace(nsName) {
				continue
			}
			var ns corev1.Namespace
			if err := c.Get(ctx, types.NamespacedName{Name: nsName}, &ns); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			if !checkNamespaceConsent(&ns, sourceNamespace, sourceName) {
				continue
			}
			out = append(out, nsName)
		}
		sort.Strings(out)
		return out, nil
	}

	var nsList corev1.NamespaceList
	if err := c.List(ctx, &nsList); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(nsList.Items))
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		nsName := ns.Name
		if nsName == sourceNamespace {
			continue
		}

		nameMatch := include.matchesNamespace(nsName)
		labelMatch := matchingSel != nil && matchingSel.Matches(labels.Set(ns.Labels))
		if !nameMatch && !labelMatch {
			continue
		}

		// kube-system protection: bypassed by explicit exact name OR label match.
		if _, protected := defaultProtectedNamespaces[nsName]; protected && !include.hasExact(nsName) && !labelMatch {
			continue
		}
		if exclude.matchesNamespace(nsName) {
			continue
		}
		if !checkNamespaceConsent(ns, sourceNamespace, sourceName) {
			continue
		}
		out = append(out, nsName)
	}
	sort.Strings(out)
	return out, nil
}

func namespaceMatchesTargeting(
	ns *corev1.Namespace,
	include targetSelector,
	exclude targetSelector,
	matchingSel labels.Selector,
	sourceNamespace string,
) bool {
	if ns == nil {
		return false
	}
	nsName := ns.Name
	if nsName == sourceNamespace {
		return false
	}

	nameMatch := include.matchesNamespace(nsName)
	labelMatch := matchingSel != nil && matchingSel.Matches(labels.Set(ns.Labels))
	if !nameMatch && !labelMatch {
		return false
	}
	if _, protected := defaultProtectedNamespaces[nsName]; protected && !include.hasExact(nsName) && !labelMatch {
		return false
	}
	if exclude.matchesNamespace(nsName) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Map copy helpers
// ---------------------------------------------------------------------------

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyByteMap(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return map[string][]byte{}
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		if v == nil {
			out[k] = nil
			continue
		}
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func filteredAnnotations(source map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range source {
		if strings.HasPrefix(k, "spillway.kroy.io/") {
			continue
		}
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Replica identity helpers
// ---------------------------------------------------------------------------

func isManagedReplica(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationManagedBy] == ManagedByValue
}

func matchesSource(obj metav1.Object, kind, namespace, name string) bool {
	ann := obj.GetAnnotations()
	return ann[AnnotationManagedBy] == ManagedByValue &&
		ann[AnnotationSourceFrom] == sourceFromValue(kind, namespace, name)
}

// isProfileReplica returns true for replicas created by a SpillwayProfile.
func isProfileReplica(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationProfileRef] != ""
}

// ---------------------------------------------------------------------------
// Finalizer helpers
// ---------------------------------------------------------------------------

func ensureFinalizer(obj client.Object) bool {
	if controllerutil.ContainsFinalizer(obj, FinalizerName) {
		return false
	}
	controllerutil.AddFinalizer(obj, FinalizerName)
	return true
}

func removeFinalizer(obj client.Object) bool {
	if !controllerutil.ContainsFinalizer(obj, FinalizerName) {
		return false
	}
	controllerutil.RemoveFinalizer(obj, FinalizerName)
	return true
}

func safeDelete(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Source descriptor
// ---------------------------------------------------------------------------

type sourceDescriptor struct {
	kind      string
	namespace string
	name      string
}

func (d sourceDescriptor) targetAnnotations(sourceAnnotations map[string]string) map[string]string {
	ann := filteredAnnotations(sourceAnnotations)
	ann[AnnotationManagedBy] = ManagedByValue
	ann[AnnotationSourceFrom] = d.sourceFrom()
	return ann
}

func (d sourceDescriptor) sourceKey() string {
	return fmt.Sprintf("%s/%s %s", d.namespace, d.name, d.kind)
}

func (d sourceDescriptor) sourceFrom() string {
	return sourceFromValue(d.kind, d.namespace, d.name)
}

func sourceFromValue(kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}

// parseSourceFrom extracts (namespace, name) from an AnnotationSourceFrom value
// of the form "Kind/namespace/name".
func parseSourceFrom(kind, raw string) (namespace, name string, ok bool) {
	prefix := kind + "/"
	if !strings.HasPrefix(raw, prefix) {
		return "", "", false
	}
	rest := raw[len(prefix):]
	idx := strings.IndexByte(rest, '/')
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

func logTargetSync(log logr.Logger, desc sourceDescriptor, targets []string) {
	log.V(1).Info("reconciling replication targets", "source", desc.sourceKey(), "targets", targets)
}

// ---------------------------------------------------------------------------
// Replica remap (annotation-based delete → source reconcile)
// ---------------------------------------------------------------------------

func sourceRequestsFromManagedReplica(log logr.Logger, obj client.Object, kind, eventName string) []reconcile.Request {
	if obj == nil {
		log.V(1).Info("replica remap failed: nil object", "kind", kind, "event", eventName)
		ReplicaRemapFailuresTotal.WithLabelValues(kind, "nil_object").Inc()
		return nil
	}

	ann := obj.GetAnnotations()
	if ann[AnnotationManagedBy] != ManagedByValue {
		return nil
	}

	ns, name, ok := parseSourceFrom(kind, ann[AnnotationSourceFrom])
	if !ok {
		log.V(1).Info(
			"replica remap failed: malformed source annotation",
			"kind", kind,
			"event", eventName,
			"namespace", obj.GetNamespace(),
			"name", obj.GetName(),
			"annotation", ann[AnnotationSourceFrom],
		)
		ReplicaRemapFailuresTotal.WithLabelValues(kind, "malformed_source_from").Inc()
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}

func enqueueReplicaDeleteSourceRemap(
	ctx context.Context,
	log logr.Logger,
	q workqueue.RateLimitingInterface,
	kind string,
	evt event.TypedDeleteEvent[client.Object],
) {
	reqs := sourceRequestsFromManagedReplica(log, evt.Object, kind, "delete")
	if len(reqs) == 0 {
		return
	}
	for _, req := range reqs {
		q.Add(req)
	}
	if evt.DeleteStateUnknown {
		log.V(1).Info("replica delete remapped from tombstone", "kind", kind, "requests", len(reqs))
	}
	_ = ctx
}

// enqueueProfileFromReplicaDelete maps a profile-replica delete event back to
// the owning SpillwayProfile so it can recreate the replica.
func enqueueProfileFromReplicaDelete(
	ctx context.Context,
	log logr.Logger,
	q workqueue.RateLimitingInterface,
	evt event.TypedDeleteEvent[client.Object],
) {
	obj := evt.Object
	if obj == nil {
		return
	}
	ref := obj.GetAnnotations()[AnnotationProfileRef]
	if ref == "" {
		return
	}
	idx := strings.IndexByte(ref, '/')
	if idx <= 0 || idx == len(ref)-1 {
		log.V(1).Info("profile replica remap failed: malformed profile-ref", "ref", ref)
		return
	}
	q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: ref[:idx],
		Name:      ref[idx+1:],
	}})
	_ = ctx
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

func sourceObjectPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object != nil && !isManagedReplica(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectNew != nil && !isManagedReplica(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil && !isManagedReplica(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return e.Object != nil && !isManagedReplica(e.Object)
		},
	}
}

// managedReplicaDeleteOnlyPredicate fires only on annotation-based replica
// deletes (not profile replicas — those are handled by profileReplicaDeleteOnlyPredicate).
func managedReplicaDeleteOnlyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return false },
		UpdateFunc: func(event.UpdateEvent) bool { return false },
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil && isManagedReplica(e.Object) && !isProfileReplica(e.Object)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// profileReplicaDeleteOnlyPredicate fires only on profile-managed replica deletes.
func profileReplicaDeleteOnlyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return false },
		UpdateFunc: func(event.UpdateEvent) bool { return false },
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil && isProfileReplica(e.Object)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func namespaceEventPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			_, ok := e.Object.(*corev1.Namespace)
			return ok
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNS, okOld := e.ObjectOld.(*corev1.Namespace)
			newNS, okNew := e.ObjectNew.(*corev1.Namespace)
			if !okOld || !okNew {
				return false
			}
			return !mapsEqual(oldNS.Labels, newNS.Labels)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Source annotation helpers
// ---------------------------------------------------------------------------

func namespaceChangeAffectsSource(obj metav1.Object, ns *corev1.Namespace) (bool, error) {
	include := listTargetSelector(obj)
	exclude := listExcludeSelector(obj)
	matchingSel, err := listMatchingSelector(obj)
	if err != nil {
		return false, err
	}
	if include.empty() && matchingSel == nil {
		return false, nil
	}
	return namespaceMatchesTargeting(ns, include, exclude, matchingSel, obj.GetNamespace()), nil
}

func replicaSourceFieldIndexValue(obj metav1.Object) []string {
	if obj == nil {
		return nil
	}
	if obj.GetAnnotations()[AnnotationManagedBy] != ManagedByValue {
		return nil
	}
	v := obj.GetAnnotations()[AnnotationSourceFrom]
	if v == "" {
		return nil
	}
	return []string{v}
}

func profileRefFieldIndexValue(obj metav1.Object) []string {
	if obj == nil {
		return nil
	}
	v := obj.GetAnnotations()[AnnotationProfileRef]
	if v == "" {
		return nil
	}
	return []string{v}
}

// ---------------------------------------------------------------------------
// Field index registration
// ---------------------------------------------------------------------------

func registerSecretReplicaIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.Secret{}, secretReplicaSourceFieldIdx, func(obj client.Object) []string {
		s, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		return replicaSourceFieldIndexValue(s)
	})
}

func registerSecretSourceTargetingIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.Secret{}, secretSourceTargetingFieldIdx, func(obj client.Object) []string {
		s, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		if sourceHasTargeting(s) {
			return []string{"true"}
		}
		return nil
	})
}

func registerConfigMapReplicaIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, configMapReplicaSourceFieldIdx, func(obj client.Object) []string {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return nil
		}
		return replicaSourceFieldIndexValue(cm)
	})
}

func registerConfigMapSourceTargetingIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, cmSourceTargetingFieldIdx, func(obj client.Object) []string {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return nil
		}
		if sourceHasTargeting(cm) {
			return []string{"true"}
		}
		return nil
	})
}

func registerSecretProfileRefIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.Secret{}, secretProfileRefFieldIdx, func(obj client.Object) []string {
		s, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		return profileRefFieldIndexValue(s)
	})
}

func registerConfigMapProfileRefIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, configMapProfileRefFieldIdx, func(obj client.Object) []string {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return nil
		}
		return profileRefFieldIndexValue(cm)
	})
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

func reconcileActionFromOperationResult(op controllerutil.OperationResult) string {
	switch op {
	case controllerutil.OperationResultCreated:
		return "create"
	case controllerutil.OperationResultUpdated, controllerutil.OperationResultUpdatedStatus, controllerutil.OperationResultUpdatedStatusOnly:
		return "update"
	default:
		return ""
	}
}

func ptrBool(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func listTargetSelector(obj metav1.Object) targetSelector {
	return parseTargetSelector(obj.GetAnnotations()[AnnotationReplicateTo])
}

func listMatchingSelector(obj metav1.Object) (labels.Selector, error) {
	raw := obj.GetAnnotations()[AnnotationReplicateToMatching]
	if raw == "" {
		return nil, nil
	}
	return labels.Parse(raw)
}

func listExcludeSelector(obj metav1.Object) targetSelector {
	return parseTargetSelector(obj.GetAnnotations()[AnnotationExcludeNS])
}

func isForceAdopt(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationForceAdopt] == "true"
}

func sourceHasTargeting(obj metav1.Object) bool {
	if obj == nil || isManagedReplica(obj) {
		return false
	}
	ann := obj.GetAnnotations()
	return strings.TrimSpace(ann[AnnotationReplicateTo]) != "" ||
		strings.TrimSpace(ann[AnnotationReplicateToMatching]) != ""
}

func ensureManagedOwnership(obj metav1.Object, desc sourceDescriptor, forceAdopt bool) error {
	if obj.GetUID() == "" {
		return nil
	}
	if matchesSource(obj, desc.kind, desc.namespace, desc.name) {
		return nil
	}
	if forceAdopt {
		return nil
	}
	return ownershipConflictError{
		objType:   fmt.Sprintf("%T", obj),
		namespace: obj.GetNamespace(),
		name:      obj.GetName(),
		sourceKey: desc.sourceKey(),
	}
}

type ownershipConflictError struct {
	objType   string
	namespace string
	name      string
	sourceKey string
}

func (e ownershipConflictError) Error() string {
	return fmt.Sprintf(
		"refusing to overwrite existing %s %s/%s that is not managed by spillway for source %s",
		e.objType, e.namespace, e.name, e.sourceKey,
	)
}

func isOwnershipConflict(err error) bool {
	var target ownershipConflictError
	return errors.As(err, &target)
}
