package controller

import "strings"

// Options holds runtime security policy settings applied to all reconcilers.
// Values are injected by main via CLI flags and are read-only after startup.
type Options struct {
	// ProtectedNamespaces is the set of namespace names that are shielded from
	// wildcard/glob/"all" replication unless the namespace is explicitly named
	// as a target. Defaults to {"kube-system"} when nil.
	ProtectedNamespaces map[string]struct{}

	// RequireNamespaceConsent, when true, changes the default namespace consent
	// model to deny-by-default: a namespace without the accept-from annotation
	// will not receive any replicas. When false (default), an absent annotation
	// means "accept from all sources" (backward-compatible behaviour).
	RequireNamespaceConsent bool

	// AllowForceAdopt, when false (default), causes the controller to ignore
	// the spillway.kroy.io/force-adopt annotation on source objects. Operators
	// must explicitly enable this flag to allow sources to take ownership of
	// pre-existing, unmanaged objects in target namespaces.
	AllowForceAdopt bool

	// AnnotationDenyPrefixes lists annotation key prefixes that are never
	// copied to replicas. This prevents policy engine, service mesh injector,
	// or workload controller annotations from leaking across namespace
	// boundaries. Defaults to DefaultAnnotationDenyPrefixes when nil.
	AnnotationDenyPrefixes []string
}

// DefaultAnnotationDenyPrefixes is the built-in list of annotation key
// prefixes that are blocked from propagating to replicas. These cover the most
// common categories of annotations that carry controller-specific meaning and
// would cause confusion or unintended side-effects if inherited by replicas.
var DefaultAnnotationDenyPrefixes = []string{
	"kubectl.kubernetes.io/",
	"meta.helm.sh/",
	"helm.sh/",
	"argocd.argoproj.io/",
	"config.kubernetes.io/",
	"kustomize.toolkit.fluxcd.io/",
	"fluxcd.io/",
	"weave.works/",
}

// effectiveProtectedNamespaces returns the protected namespace set to use,
// falling back to {"kube-system"} when the Options field is nil/empty.
func (o Options) effectiveProtectedNamespaces() map[string]struct{} {
	if len(o.ProtectedNamespaces) > 0 {
		return o.ProtectedNamespaces
	}
	return map[string]struct{}{"kube-system": {}}
}

// effectiveAnnotationDenyPrefixes returns the deny prefix list to use,
// falling back to DefaultAnnotationDenyPrefixes when nil.
func (o Options) effectiveAnnotationDenyPrefixes() []string {
	if o.AnnotationDenyPrefixes != nil {
		return o.AnnotationDenyPrefixes
	}
	return DefaultAnnotationDenyPrefixes
}

// ParseProtectedNamespaces splits a comma-separated string into a name set.
func ParseProtectedNamespaces(raw string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, ns := range strings.Split(raw, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			m[ns] = struct{}{}
		}
	}
	return m
}

// ParseAnnotationDenyPrefixes splits a comma-separated string into a prefix slice.
func ParseAnnotationDenyPrefixes(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
