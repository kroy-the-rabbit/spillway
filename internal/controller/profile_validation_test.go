package controller

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	spillwayv1 "spillway/api/v1"
	spillwayv1alpha1 "spillway/api/v1alpha1"
)

// generatedCRDPath is the controller-gen output and the single source of truth
// for the SpillwayProfile schema. The chart copy is derived from it by
// hack/sync-crd.sh.
var generatedCRDPath = filepath.Join("..", "..", "config", "crd", "spillway.kroy.io_spillwayprofiles.yaml")

// crdFiles lists every copy of the CRD that ships with the repository.
var crdFiles = []string{
	generatedCRDPath,
	filepath.Join("..", "..", "charts", "spillway", "templates", "crd.yaml"),
}

// crdVersions is the expected served version list, in the order controller-gen
// emits it. v1 is the storage version; v1alpha1 is served for compatibility.
var crdVersions = []string{"v1", "v1alpha1"}

// v1alpha1DeprecationWarning must match the +kubebuilder:deprecatedversion
// marker on api/v1alpha1.SpillwayProfile.
const v1alpha1DeprecationWarning = "spillway.kroy.io/v1alpha1 SpillwayProfile is deprecated; use spillway.kroy.io/v1"

// loadCRD parses a CRD manifest. Helm template directives ({{ ... }}) are
// dropped line-wise so the chart template can be parsed without rendering.
func loadCRD(t *testing.T, path string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.Contains(line, "{{") {
			continue
		}
		kept = append(kept, line)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.UnmarshalStrict([]byte(strings.Join(kept, "\n")), crd); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return crd
}

// crdVersion returns the named version from the CRD or fails the test.
func crdVersion(t *testing.T, file string, crd *apiextensionsv1.CustomResourceDefinition, name string) apiextensionsv1.CustomResourceDefinitionVersion {
	t.Helper()
	for _, v := range crd.Spec.Versions {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s: version %q not found in %+v", file, name, crd.Spec.Versions)
	return apiextensionsv1.CustomResourceDefinitionVersion{}
}

func TestSpillwayProfileCRDVersions(t *testing.T) {
	for _, file := range crdFiles {
		crd := loadCRD(t, file)
		if crd.Name != "spillwayprofiles.spillway.kroy.io" {
			t.Fatalf("%s: unexpected CRD name %q", file, crd.Name)
		}

		var names []string
		for _, v := range crd.Spec.Versions {
			names = append(names, v.Name)
		}
		if !slices.Equal(names, crdVersions) {
			t.Fatalf("%s: expected versions %v, got %v", file, crdVersions, names)
		}

		v1 := crdVersion(t, file, crd, "v1")
		if !v1.Served {
			t.Fatalf("%s: v1 must be served", file)
		}
		if !v1.Storage {
			t.Fatalf("%s: v1 must be the storage version", file)
		}
		if v1.Deprecated || v1.DeprecationWarning != nil {
			t.Fatalf("%s: v1 must not be deprecated, got deprecated=%v warning=%v", file, v1.Deprecated, v1.DeprecationWarning)
		}

		v1alpha1 := crdVersion(t, file, crd, "v1alpha1")
		if !v1alpha1.Served {
			t.Fatalf("%s: v1alpha1 must still be served for compatibility", file)
		}
		if v1alpha1.Storage {
			t.Fatalf("%s: v1alpha1 must not be the storage version", file)
		}
		if !v1alpha1.Deprecated {
			t.Fatalf("%s: v1alpha1 must be marked deprecated", file)
		}
		if v1alpha1.DeprecationWarning == nil || *v1alpha1.DeprecationWarning != v1alpha1DeprecationWarning {
			t.Fatalf("%s: v1alpha1 deprecationWarning = %v, want %q", file, v1alpha1.DeprecationWarning, v1alpha1DeprecationWarning)
		}

		// The two versions share one Go type shape, so they must expose an
		// identical schema and identical subresources. That is what makes
		// conversion strategy None (the apiextensions default) correct.
		if !reflect.DeepEqual(v1.Schema, v1alpha1.Schema) {
			t.Fatalf("%s: v1 and v1alpha1 schemas differ; conversion strategy None requires identical schemas", file)
		}
		if !reflect.DeepEqual(v1.Subresources, v1alpha1.Subresources) {
			t.Fatalf("%s: v1 and v1alpha1 subresources differ: %+v vs %+v", file, v1.Subresources, v1alpha1.Subresources)
		}
		if !reflect.DeepEqual(v1.AdditionalPrinterColumns, v1alpha1.AdditionalPrinterColumns) {
			t.Fatalf("%s: v1 and v1alpha1 printer columns differ", file)
		}
		if crd.Spec.Conversion != nil && crd.Spec.Conversion.Strategy != apiextensionsv1.NoneConverter {
			t.Fatalf("%s: conversion strategy must be None (no webhook), got %+v", file, crd.Spec.Conversion)
		}
	}
}

func TestSpillwayProfileCRDContainsKeyValidation(t *testing.T) {
	for _, file := range crdFiles {
		crd := loadCRD(t, file)
		if crd.Name != "spillwayprofiles.spillway.kroy.io" {
			t.Fatalf("%s: unexpected CRD name %q", file, crd.Name)
		}
		if len(crd.Spec.Versions) != len(crdVersions) {
			t.Fatalf("%s: expected %d versions %v, got %+v", file, len(crdVersions), crdVersions, crd.Spec.Versions)
		}
		for _, v := range crd.Spec.Versions {
			where := file + " version " + v.Name
			if v.Subresources == nil || v.Subresources.Status == nil {
				t.Fatalf("%s: status subresource must be enabled", where)
			}
			if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
				t.Fatalf("%s: missing openAPIV3Schema", where)
			}
			spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
			if !slices.Contains(spec.Required, "sources") {
				t.Fatalf("%s: spec.sources must be required, got %v", where, spec.Required)
			}
			sources := spec.Properties["sources"]
			if sources.XListType == nil || *sources.XListType != "map" {
				t.Fatalf("%s: sources must be x-kubernetes-list-type: map", where)
			}
			if !slices.Equal(sources.XListMapKeys, []string{"kind", "name"}) {
				t.Fatalf("%s: sources list map keys must be [kind name], got %v", where, sources.XListMapKeys)
			}
			if sources.MinItems == nil || *sources.MinItems != 1 {
				t.Fatalf("%s: sources must have minItems: 1", where)
			}
			if sources.Items == nil || sources.Items.Schema == nil {
				t.Fatalf("%s: sources items schema missing", where)
			}
			item := sources.Items.Schema
			found := false
			for _, rule := range item.XValidations {
				if rule.Message == "includeKeys and excludeKeys are mutually exclusive" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: sources items missing includeKeys/excludeKeys CEL rule, got %+v", where, item.XValidations)
			}
			kind := item.Properties["kind"]
			if len(kind.Enum) != 2 {
				t.Fatalf("%s: sources[].kind must be an enum of Secret and ConfigMap, got %v", where, kind.Enum)
			}
			name := item.Properties["name"]
			if name.MinLength == nil || *name.MinLength != 1 {
				t.Fatalf("%s: sources[].name must have minLength: 1", where)
			}
		}
	}
}

// startCRDEnvtest starts an envtest API server with the generated CRD
// installed and registers a stop hook on the test.
func startCRDEnvtest(t *testing.T) *rest.Config {
	t.Helper()
	crdDir, err := filepath.Abs(filepath.Join("..", "..", "config", "crd"))
	if err != nil {
		t.Fatalf("abs crd dir: %v", err)
	}
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{crdDir},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		if envtestUnavailable(err) {
			t.Skipf("envtest binaries unavailable: %v", err)
		}
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			t.Fatalf("stop envtest: %v", stopErr)
		}
	})
	return cfg
}

func TestSpillwayProfileEnvtest_RejectsIncludeAndExcludeKeysTogether(t *testing.T) {
	ctx := context.Background()
	cfg := startCRDEnvtest(t)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if err := k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
	}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	newProfile := func(name string, spec map[string]any) *unstructured.Unstructured {
		profile := &unstructured.Unstructured{}
		profile.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "spillway.kroy.io",
			Version: "v1",
			Kind:    "SpillwayProfile",
		})
		profile.SetNamespace("platform")
		profile.SetName(name)
		profile.Object["spec"] = spec
		return profile
	}

	invalid := []struct {
		name string
		spec map[string]any
	}{
		{
			name: "include-and-exclude-keys",
			spec: map[string]any{
				"targetNamespaces": []any{"team-a"},
				"sources": []any{
					map[string]any{
						"kind":        "Secret",
						"name":        "shared-token",
						"includeKeys": []any{"token"},
						"excludeKeys": []any{"token"},
					},
				},
			},
		},
		{
			name: "empty-sources",
			spec: map[string]any{
				"targetNamespaces": []any{"team-a"},
				"sources":          []any{},
			},
		},
		{
			name: "bad-kind",
			spec: map[string]any{
				"sources": []any{
					map[string]any{"kind": "Pod", "name": "x"},
				},
			},
		},
		{
			name: "bad-selector-operator",
			spec: map[string]any{
				"targetSelector": map[string]any{
					"matchExpressions": []any{
						map[string]any{"key": "team", "operator": "Equals", "values": []any{"a"}},
					},
				},
				"sources": []any{
					map[string]any{"kind": "Secret", "name": "x"},
				},
			},
		},
	}
	for _, tc := range invalid {
		err := k8sClient.Create(ctx, newProfile(tc.name, tc.spec))
		if err == nil {
			t.Fatalf("%s: expected invalid SpillwayProfile to be rejected by CRD validation", tc.name)
		}
		if !apierrors.IsInvalid(err) {
			t.Fatalf("%s: expected invalid error, got: %v", tc.name, err)
		}
	}

	valid := newProfile("valid-profile", map[string]any{
		"targetSelector": map[string]any{
			"matchExpressions": []any{
				map[string]any{"key": "team", "operator": "In", "values": []any{"a"}},
			},
		},
		"sources": []any{
			map[string]any{"kind": "ConfigMap", "name": "shared-env", "includeKeys": []any{"A"}},
		},
	})
	if err := k8sClient.Create(ctx, valid); err != nil {
		t.Fatalf("expected valid SpillwayProfile to be accepted, got: %v", err)
	}
}

// warningRecorder captures API server Warning headers so a test can assert
// that a deprecated version was announced to the client.
type warningRecorder struct {
	mu       sync.Mutex
	warnings []string
}

func (w *warningRecorder) HandleWarningHeaderWithContext(_ context.Context, _ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, text)
}

func (w *warningRecorder) contains(text string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Contains(w.warnings, text)
}

func (w *warningRecorder) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.warnings)
}

// TestSpillwayProfileEnvtest_V1alpha1RoundTripsToV1 proves the upgrade
// contract of the CRD: a profile written through the deprecated v1alpha1
// endpoint is stored as v1 and readable through both endpoints with the same
// content, the v1alpha1 endpoint emits the deprecation warning, and the
// installed CRD carries the deprecation metadata and v1 as its only stored
// version on a fresh install.
func TestSpillwayProfileEnvtest_V1alpha1RoundTripsToV1(t *testing.T) {
	ctx := context.Background()
	cfg := rest.CopyConfig(startCRDEnvtest(t))

	recorder := &warningRecorder{}
	cfg.WarningHandlerWithContext = recorder

	scheme := runtime.NewScheme()
	for name, add := range map[string]func(*runtime.Scheme) error{
		"core":          corev1.AddToScheme,
		"apiextensions": apiextensionsv1.AddToScheme,
		"v1":            spillwayv1.AddToScheme,
		"v1alpha1":      spillwayv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add %s scheme: %v", name, err)
		}
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if err := k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
	}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	// The installed CRD must expose both versions with v1 as storage and
	// v1alpha1 deprecated. A fresh install stores only v1.
	var crd apiextensionsv1.CustomResourceDefinition
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "spillwayprofiles.spillway.kroy.io"}, &crd); err != nil {
		t.Fatalf("get CRD: %v", err)
	}
	var names []string
	for _, v := range crd.Spec.Versions {
		names = append(names, v.Name)
		switch v.Name {
		case "v1":
			if !v.Served || !v.Storage || v.Deprecated {
				t.Fatalf("installed CRD v1: served=%v storage=%v deprecated=%v", v.Served, v.Storage, v.Deprecated)
			}
		case "v1alpha1":
			if !v.Served || v.Storage || !v.Deprecated {
				t.Fatalf("installed CRD v1alpha1: served=%v storage=%v deprecated=%v", v.Served, v.Storage, v.Deprecated)
			}
			if v.DeprecationWarning == nil || *v.DeprecationWarning != v1alpha1DeprecationWarning {
				t.Fatalf("installed CRD v1alpha1 deprecationWarning = %v, want %q", v.DeprecationWarning, v1alpha1DeprecationWarning)
			}
		}
	}
	if !slices.Equal(names, crdVersions) {
		t.Fatalf("installed CRD versions = %v, want %v", names, crdVersions)
	}
	if !slices.Equal(crd.Status.StoredVersions, []string{"v1"}) {
		t.Fatalf("fresh install storedVersions = %v, want [v1]", crd.Status.StoredVersions)
	}

	// Write through the deprecated endpoint, exactly as a client on the
	// previous release would.
	old := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces:  []string{"team-*"},
			ExcludeNamespaces: []string{"team-legacy"},
			TargetSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"environment": "production"},
			},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "registry-pull-secret"},
				{Kind: "ConfigMap", Name: "shared-env", ExcludeKeys: []string{"SENTRY_DSN"}},
			},
		},
	}
	if err := k8sClient.Create(ctx, old); err != nil {
		t.Fatalf("create v1alpha1 profile: %v", err)
	}
	if !recorder.contains(v1alpha1DeprecationWarning) {
		t.Fatalf("expected deprecation warning %q on v1alpha1 create, got warnings %v", v1alpha1DeprecationWarning, recorder.all())
	}

	// Read back through the v1 endpoint: same object, same spec.
	var current spillwayv1.SpillwayProfile
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(old), &current); err != nil {
		t.Fatalf("get profile as v1: %v", err)
	}
	if current.UID != old.UID {
		t.Fatalf("v1 read returned a different object: uid %s vs %s", current.UID, old.UID)
	}
	if current.APIVersion != "" && current.APIVersion != spillwayv1.GroupVersion.String() {
		t.Fatalf("v1 read reported apiVersion %q", current.APIVersion)
	}
	wantSpec := spillwayv1.SpillwayProfileSpec{
		TargetNamespaces:  old.Spec.TargetNamespaces,
		ExcludeNamespaces: old.Spec.ExcludeNamespaces,
		TargetSelector:    old.Spec.TargetSelector,
		Sources: []spillwayv1.ProfileSource{
			{Kind: "Secret", Name: "registry-pull-secret"},
			{Kind: "ConfigMap", Name: "shared-env", ExcludeKeys: []string{"SENTRY_DSN"}},
		},
	}
	if !reflect.DeepEqual(current.Spec, wantSpec) {
		t.Fatalf("v1 spec after v1alpha1 create = %+v, want %+v", current.Spec, wantSpec)
	}

	// Status writes on v1 (what the controller does) are visible on v1alpha1.
	current.Status.ReplicatedNamespaces = []string{"team-a"}
	if err := k8sClient.Status().Update(ctx, &current); err != nil {
		t.Fatalf("update status as v1: %v", err)
	}
	var back spillwayv1alpha1.SpillwayProfile
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(old), &back); err != nil {
		t.Fatalf("get profile as v1alpha1: %v", err)
	}
	if !slices.Equal(back.Status.ReplicatedNamespaces, []string{"team-a"}) {
		t.Fatalf("v1alpha1 read after v1 status update: replicatedNamespaces = %v", back.Status.ReplicatedNamespaces)
	}

	// Listing through v1 sees the object created through v1alpha1.
	var list spillwayv1.SpillwayProfileList
	if err := k8sClient.List(ctx, &list, client.InNamespace("platform")); err != nil {
		t.Fatalf("list profiles as v1: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "legacy" {
		t.Fatalf("v1 list = %d items, want the single legacy profile: %+v", len(list.Items), list.Items)
	}

	// The v1 endpoint itself never warns.
	before := len(recorder.all())
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(old), &current); err != nil {
		t.Fatalf("get profile as v1: %v", err)
	}
	if after := recorder.all(); len(after) != before {
		t.Fatalf("v1 read emitted warnings: %v", after[before:])
	}

	// Storage stays v1 only: writing through v1alpha1 must not add it to
	// storedVersions, because the API server persists at the storage version.
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "spillwayprofiles.spillway.kroy.io"}, &crd); err != nil {
		t.Fatalf("get CRD: %v", err)
	}
	if !slices.Equal(crd.Status.StoredVersions, []string{"v1"}) {
		t.Fatalf("storedVersions after v1alpha1 write = %v, want [v1]", crd.Status.StoredVersions)
	}
}
