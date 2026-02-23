package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTargetSelectorMatches(t *testing.T) {
	sel := parseTargetSelector("team-*,sandbox,all")
	if sel.empty() {
		t.Fatalf("selector should not be empty")
	}
	for _, ns := range []string{"team-a", "sandbox", "prod"} {
		if !sel.matchesNamespace(ns) {
			t.Fatalf("expected selector to match %q", ns)
		}
	}
}

func TestResolveTargetNamespaces_DefaultProtectedAndExclude(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMeta("team-a")},
		&corev1.Namespace{ObjectMeta: objectMeta("team-dev")},
		&corev1.Namespace{ObjectMeta: objectMeta("kube-system")},
	).Build()

	include := parseTargetSelector("all")
	exclude := parseTargetSelector("team-dev")

	got, err := resolveTargetNamespaces(context.Background(), c, include, exclude, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}

	want := []string{"team-a"}
	if len(got) != len(want) {
		t.Fatalf("unexpected targets: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected targets: got=%v want=%v", got, want)
		}
	}
}

func TestResolveTargetNamespaces_ExplicitKubeSystemOverride(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMeta("kube-system")},
	).Build()

	got, err := resolveTargetNamespaces(context.Background(), c, parseTargetSelector("kube-* , kube-system"), targetSelector{}, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(got) != 1 || got[0] != "kube-system" {
		t.Fatalf("expected explicit kube-system include override, got=%v", got)
	}
}

func TestSourceFromValue(t *testing.T) {
	got := sourceFromValue("Secret", "platform", "api-token")
	if got != "Secret/platform/api-token" {
		t.Fatalf("unexpected source-from value: %q", got)
	}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}
