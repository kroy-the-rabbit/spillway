package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestConfigMapReconcileSmoke_AllRespectsDefaultExcludes(t *testing.T) {
	ctx := context.Background()
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "all",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test")}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-config"}, &replicated); err != nil {
		t.Fatalf("get replicated configmap: %v", err)
	}
	if replicated.Annotations[AnnotationManagedBy] != ManagedByValue {
		t.Fatalf("expected managed-by annotation, got %q", replicated.Annotations[AnnotationManagedBy])
	}

	var kubeSystemCopy corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "shared-config"}, &kubeSystemCopy)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no kube-system replica, got err=%v", err)
	}
}

func TestConfigMapReconcileSmoke_ExplicitKubeSystemIncludeOverridesDefault(t *testing.T) {
	ctx := context.Background()
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "kube-*,kube-system",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test")}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "shared-config"}, &replicated); err != nil {
		t.Fatalf("expected kube-system replica from explicit include override: %v", err)
	}
}

func TestConfigMapReconcileSmoke_LabelSelectorTargeting(t *testing.T) {
	ctx := context.Background()
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "team-infra",
			Labels: map[string]string{"team": "platform"},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-other"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateToMatching: "team=platform",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test")}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-infra", Name: "shared-config"}, &replicated); err != nil {
		t.Fatalf("expected replica in team-infra: %v", err)
	}

	var notReplicated corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: "team-other", Name: "shared-config"}, &notReplicated)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no replica in team-other, got err=%v", err)
	}
}

func TestConfigMapReconcileSmoke_ManagedByLabelIsSet(t *testing.T) {
	ctx := context.Background()
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test")}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-config"}, &replicated); err != nil {
		t.Fatalf("get replicated configmap: %v", err)
	}
	if replicated.Labels[LabelManagedBy] != ManagedByValue {
		t.Fatalf("expected managed-by label %q=%q, got %q", LabelManagedBy, ManagedByValue, replicated.Labels[LabelManagedBy])
	}
}

func newCMSmokeClient(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), scheme
}
