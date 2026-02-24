package controller

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

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
	// previously managed by spillway. Use this to take ownership of objects
	// that existed before spillway was installed.
	AnnotationForceAdopt = "spillway.kroy.io/force-adopt"

	AnnotationManagedBy  = "spillway.kroy.io/managed-by"
	AnnotationSourceFrom = "spillway.kroy.io/source-from"

	// LabelManagedBy is the same key as AnnotationManagedBy, set as a label
	// on replica objects so cleanup can use a label-filtered List.
	LabelManagedBy = "spillway.kroy.io/managed-by"

	ManagedByValue = "spillway"
	FinalizerName  = "spillway.kroy.io/finalizer"

	secretReplicaSourceFieldIdx    = "spillway.kroy.io/index.secret-source-from"
	configMapReplicaSourceFieldIdx = "spillway.kroy.io/index.configmap-source-from"
)

var defaultProtectedNamespaces = map[string]struct{}{
	"kube-system": {},
}

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

func resolveTargetNamespaces(
	ctx context.Context,
	c client.Client,
	include targetSelector,
	exclude targetSelector,
	matchingSel labels.Selector,
	sourceNamespace string,
) ([]string, error) {
	if include.empty() && matchingSel == nil {
		return nil, nil
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

		// kube-system protection: bypassed by explicit exact name OR label match
		if _, protected := defaultProtectedNamespaces[nsName]; protected && !include.hasExact(nsName) && !labelMatch {
			continue
		}
		if exclude.matchesNamespace(nsName) {
			continue
		}
		out = append(out, nsName)
	}
	sort.Strings(out)
	return out, nil
}

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

func isManagedReplica(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationManagedBy] == ManagedByValue
}

func matchesSource(obj metav1.Object, kind, namespace, name string) bool {
	ann := obj.GetAnnotations()
	return ann[AnnotationManagedBy] == ManagedByValue &&
		ann[AnnotationSourceFrom] == sourceFromValue(kind, namespace, name)
}

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

// parseSourceFrom extracts (namespace, name) from a AnnotationSourceFrom value
// of the form "Kind/namespace/name". Returns ok=false if the value doesn't
// match the expected kind or is malformed.
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

func logTargetSync(log logr.Logger, desc sourceDescriptor, targets []string) {
	log.Info("reconciling replication targets", "source", desc.sourceKey(), "targets", targets)
}

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

func managedReplicaDeleteOnlyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return false },
		UpdateFunc: func(event.UpdateEvent) bool { return false },
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil && isManagedReplica(e.Object)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
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

func registerSecretReplicaIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &corev1.Secret{}, secretReplicaSourceFieldIdx, func(obj client.Object) []string {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		return replicaSourceFieldIndexValue(secret)
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
	return fmt.Errorf("refusing to overwrite existing %T %s/%s that is not managed by spillway for source %s", obj, obj.GetNamespace(), obj.GetName(), desc.sourceKey())
}
