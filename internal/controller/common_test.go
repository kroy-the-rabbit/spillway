package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

	got, err := resolveTargetNamespaces(context.Background(), c, include, exclude, nil, "source")
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

	got, err := resolveTargetNamespaces(context.Background(), c, parseTargetSelector("kube-* , kube-system"), targetSelector{}, nil, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(got) != 1 || got[0] != "kube-system" {
		t.Fatalf("expected explicit kube-system include override, got=%v", got)
	}
}

func TestResolveTargetNamespaces_LabelSelector(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMetaLabels("team-platform", map[string]string{"team": "platform"})},
		&corev1.Namespace{ObjectMeta: objectMeta("team-other")},
	).Build()

	sel, err := labels.Parse("team=platform")
	if err != nil {
		t.Fatalf("parse label selector: %v", err)
	}

	got, err := resolveTargetNamespaces(context.Background(), c, targetSelector{}, targetSelector{}, sel, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(got) != 1 || got[0] != "team-platform" {
		t.Fatalf("expected [team-platform], got=%v", got)
	}
}

func TestResolveTargetNamespaces_LabelSelectorWithExclude(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMetaLabels("team-a", map[string]string{"team": "platform"})},
		&corev1.Namespace{ObjectMeta: objectMetaLabels("team-b", map[string]string{"team": "platform"})},
	).Build()

	sel, err := labels.Parse("team=platform")
	if err != nil {
		t.Fatalf("parse label selector: %v", err)
	}
	exclude := parseTargetSelector("team-b")

	got, err := resolveTargetNamespaces(context.Background(), c, targetSelector{}, exclude, sel, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(got) != 1 || got[0] != "team-a" {
		t.Fatalf("expected [team-a], got=%v", got)
	}
}

func TestResolveTargetNamespaces_LabelAndNameUnion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMetaLabels("team-a", map[string]string{"team": "platform"})},
		&corev1.Namespace{ObjectMeta: objectMeta("explicit-ns")},
		&corev1.Namespace{ObjectMeta: objectMeta("unrelated")},
	).Build()

	sel, err := labels.Parse("team=platform")
	if err != nil {
		t.Fatalf("parse label selector: %v", err)
	}
	include := parseTargetSelector("explicit-ns")

	got, err := resolveTargetNamespaces(context.Background(), c, include, targetSelector{}, sel, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	want := []string{"explicit-ns", "team-a"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got=%v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got=%v", want, got)
		}
	}
}

func TestResolveTargetNamespaces_LabelSelectorKubeSystemBypass(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: objectMeta("source")},
		&corev1.Namespace{ObjectMeta: objectMetaLabels("kube-system", map[string]string{"spillway-target": "yes"})},
	).Build()

	sel, err := labels.Parse("spillway-target=yes")
	if err != nil {
		t.Fatalf("parse label selector: %v", err)
	}

	got, err := resolveTargetNamespaces(context.Background(), c, targetSelector{}, targetSelector{}, sel, "source")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(got) != 1 || got[0] != "kube-system" {
		t.Fatalf("expected label match to bypass kube-system protection, got=%v", got)
	}
}

func TestListMatchingSelector_InvalidReturnsError(t *testing.T) {
	obj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationReplicateToMatching: "!!!invalid!!!",
			},
		},
	}
	_, err := listMatchingSelector(obj)
	if err == nil {
		t.Fatal("expected error for invalid label selector, got nil")
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

func objectMetaLabels(name string, lbls map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Labels: lbls}
}
