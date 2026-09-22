package controller

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// generatedCRDPath is the controller-gen output and the single source of truth
// for the SpillwayProfile schema. The chart copy is derived from it by
// hack/sync-crd.sh.
var generatedCRDPath = filepath.Join("..", "..", "config", "crd", "spillway.kroy.io_spillwayprofiles.yaml")

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

func TestSpillwayProfileCRDContainsKeyValidation(t *testing.T) {
	files := []string{
		generatedCRDPath,
		filepath.Join("..", "..", "charts", "spillway", "templates", "crd.yaml"),
	}

	for _, file := range files {
		crd := loadCRD(t, file)
		if crd.Name != "spillwayprofiles.spillway.kroy.io" {
			t.Fatalf("%s: unexpected CRD name %q", file, crd.Name)
		}
		if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Name != "v1alpha1" {
			t.Fatalf("%s: expected exactly one version v1alpha1, got %+v", file, crd.Spec.Versions)
		}
		v := crd.Spec.Versions[0]
		if v.Subresources == nil || v.Subresources.Status == nil {
			t.Fatalf("%s: status subresource must be enabled", file)
		}
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			t.Fatalf("%s: missing openAPIV3Schema", file)
		}
		spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !slices.Contains(spec.Required, "sources") {
			t.Fatalf("%s: spec.sources must be required, got %v", file, spec.Required)
		}
		sources := spec.Properties["sources"]
		if sources.XListType == nil || *sources.XListType != "map" {
			t.Fatalf("%s: sources must be x-kubernetes-list-type: map", file)
		}
		if !slices.Equal(sources.XListMapKeys, []string{"kind", "name"}) {
			t.Fatalf("%s: sources list map keys must be [kind name], got %v", file, sources.XListMapKeys)
		}
		if sources.MinItems == nil || *sources.MinItems != 1 {
			t.Fatalf("%s: sources must have minItems: 1", file)
		}
		if sources.Items == nil || sources.Items.Schema == nil {
			t.Fatalf("%s: sources items schema missing", file)
		}
		item := sources.Items.Schema
		found := false
		for _, rule := range item.XValidations {
			if rule.Message == "includeKeys and excludeKeys are mutually exclusive" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: sources items missing includeKeys/excludeKeys CEL rule, got %+v", file, item.XValidations)
		}
		kind := item.Properties["kind"]
		if len(kind.Enum) != 2 {
			t.Fatalf("%s: sources[].kind must be an enum of Secret and ConfigMap, got %v", file, kind.Enum)
		}
		name := item.Properties["name"]
		if name.MinLength == nil || *name.MinLength != 1 {
			t.Fatalf("%s: sources[].name must have minLength: 1", file)
		}
	}
}

func TestSpillwayProfileEnvtest_RejectsIncludeAndExcludeKeysTogether(t *testing.T) {
	ctx := context.Background()

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
	defer func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			t.Fatalf("stop envtest: %v", stopErr)
		}
	}()

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
			Version: "v1alpha1",
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
