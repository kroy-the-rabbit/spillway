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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	AnnotationReplicateTo = "spillway.kroy.io/replicate-to"
	AnnotationExcludeNS   = "spillway.kroy.io/exclude-namespaces"

	// LabelDefaultReplicate on a Namespace sets the default replication target
	// selector for all Secrets and ConfigMaps in that namespace that have no
	// per-object spillway.kroy.io/replicate-to annotation.
	LabelDefaultReplicate = "spillway.kroy.io/default-replicate"

	AnnotationManagedBy  = "spillway.kroy.io/managed-by"
	AnnotationSourceFrom = "spillway.kroy.io/source-from"

	ManagedByValue = "spillway"
	FinalizerName  = "spillway.kroy.io/finalizer"
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
	sourceNamespace string,
) ([]string, error) {
	if include.empty() {
		return nil, nil
	}

	var nsList corev1.NamespaceList
	if err := c.List(ctx, &nsList); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(nsList.Items))
	for i := range nsList.Items {
		ns := nsList.Items[i].Name
		if ns == sourceNamespace {
			continue
		}
		if !include.matchesNamespace(ns) {
			continue
		}
		if _, protected := defaultProtectedNamespaces[ns]; protected && !include.hasExact(ns) {
			continue
		}
		if exclude.matchesNamespace(ns) {
			continue
		}
		out = append(out, ns)
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

func logTargetSync(log logr.Logger, desc sourceDescriptor, targets []string) {
	log.Info("reconciling replication targets", "source", desc.sourceKey(), "targets", targets)
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

func listExcludeSelector(obj metav1.Object) targetSelector {
	return parseTargetSelector(obj.GetAnnotations()[AnnotationExcludeNS])
}

// nsDefaultSelector returns the replication target selector derived from the
// namespace's LabelDefaultReplicate label. Returns an empty selector when the
// label is absent or blank.
func nsDefaultSelector(ns corev1.Namespace) targetSelector {
	return parseTargetSelector(ns.Labels[LabelDefaultReplicate])
}

func ensureManagedOwnership(obj metav1.Object, desc sourceDescriptor) error {
	if obj.GetUID() == "" {
		return nil
	}
	if matchesSource(obj, desc.kind, desc.namespace, desc.name) {
		return nil
	}
	return fmt.Errorf("refusing to overwrite existing %T %s/%s that is not managed by spillway for source %s", obj, obj.GetNamespace(), obj.GetName(), desc.sourceKey())
}
