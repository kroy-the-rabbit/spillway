package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestSpillwayProfileCRDContainsKeyValidation(t *testing.T) {
	t.Helper()

	files := []string{
		filepath.Join("..", "..", "config", "crd", "spillwayprofile.yaml"),
		filepath.Join("..", "..", "charts", "spillway", "templates", "crd.yaml"),
	}
	required := []string{
		`x-kubernetes-validations:`,
		`includeKeys and excludeKeys are mutually exclusive`,
		`x-kubernetes-list-type: map`,
		`x-kubernetes-list-map-keys: ["kind", "name"]`,
	}

	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(body)
		for _, needle := range required {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s is missing required schema fragment %q", file, needle)
			}
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

	profile := &unstructured.Unstructured{}
	profile.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "spillway.kroy.io",
		Version: "v1alpha1",
		Kind:    "SpillwayProfile",
	})
	profile.SetNamespace("platform")
	profile.SetName("invalid-profile")
	profile.Object["spec"] = map[string]any{
		"targetNamespaces": []any{"team-a"},
		"sources": []any{
			map[string]any{
				"kind":        "Secret",
				"name":        "shared-token",
				"includeKeys": []any{"token"},
				"excludeKeys": []any{"token"},
			},
		},
	}

	err = k8sClient.Create(ctx, profile)
	if err == nil {
		t.Fatal("expected invalid SpillwayProfile to be rejected by CRD validation")
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected invalid error, got: %v", err)
	}
}
