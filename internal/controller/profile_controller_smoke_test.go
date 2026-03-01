package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	spillwayv1alpha1 "spillway/api/v1alpha1"
)

func newProfileScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := spillwayv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1 scheme: %v", err)
	}
	return s
}

func newProfileClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := newProfileScheme(t)
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&spillwayv1alpha1.SpillwayProfile{}).
		WithIndex(&corev1.Secret{}, secretProfileRefFieldIdx, func(obj client.Object) []string {
			return profileRefFieldIndexValue(obj)
		}).
		WithIndex(&corev1.ConfigMap{}, configMapProfileRefFieldIdx, func(obj client.Object) []string {
			return profileRefFieldIndexValue(obj)
		}).
		Build()
}

func newProfileRec(t *testing.T, c client.WithWatch) *ProfileReconciler {
	t.Helper()
	return &ProfileReconciler{
		Client:   c,
		Scheme:   newProfileScheme(t),
		Log:      logr.Discard(),
		Recorder: record.NewFakeRecorder(100),
	}
}

func profileReq(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

func TestProfileReconcileSmoke_ReplicatesSecretToTargetNamespace(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "platform"},
		Data: map[string][]byte{
			"token": []byte("s3cr3t"),
			"extra": []byte("ignore-me"),
		},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "my-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "platform-token", IncludeKeys: []string{"token"}},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		srcSecret, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "my-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "platform-token"}, &replica); err != nil {
		t.Fatalf("replica not found: %v", err)
	}
	if _, ok := replica.Data["extra"]; ok {
		t.Error("expected 'extra' key to be filtered out by includeKeys")
	}
	if string(replica.Data["token"]) != "s3cr3t" {
		t.Errorf("expected token='s3cr3t', got %q", replica.Data["token"])
	}
	if replica.Annotations[AnnotationProfileRef] != "platform/my-profile" {
		t.Errorf("expected profile-ref annotation, got %q", replica.Annotations[AnnotationProfileRef])
	}
	if replica.Labels[LabelManagedBy] != ManagedByValue {
		t.Errorf("expected managed-by label, got %q", replica.Labels[LabelManagedBy])
	}
}

func TestProfileReconcileSmoke_ReplicatesConfigMapToTargetNamespace(t *testing.T) {
	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "platform"},
		Data:       map[string]string{"host": "db.internal", "port": "5432"},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-b"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "ConfigMap", Name: "shared-config"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		srcCM, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "cfg-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-b", Name: "shared-config"}, &replica); err != nil {
		t.Fatalf("replica not found: %v", err)
	}
	if replica.Data["host"] != "db.internal" {
		t.Errorf("unexpected data: %v", replica.Data)
	}
	if replica.Annotations[AnnotationProfileRef] != "platform/cfg-profile" {
		t.Errorf("expected profile-ref annotation, got %q", replica.Annotations[AnnotationProfileRef])
	}
}

func TestProfileReconcileSmoke_CleanupOnDeletion(t *testing.T) {
	profileRef := "platform/my-profile"
	existingReplica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-token",
			Namespace: "team-a",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationProfileRef: profileRef,
			},
		},
	}
	now := metav1.Now()
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-profile",
			Namespace:         "platform",
			DeletionTimestamp: &now,
			Finalizers:        []string{FinalizerName},
		},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "platform-token"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		existingReplica, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "my-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.Secret
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "platform-token"}, &replica)
	if err == nil {
		t.Fatal("expected replica to be deleted, but it still exists")
	}
}

func TestProfileReconcileSmoke_SkipsMissingSource(t *testing.T) {
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "my-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "does-not-exist"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "my-profile")); err != nil {
		t.Fatalf("reconcile should not error on missing source, got: %v", err)
	}
}

func TestProfileReconcileSmoke_ExcludeKeysFilter(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "platform"},
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("secret"),
			"host":     []byte("db.internal"),
		},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "creds-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "creds", ExcludeKeys: []string{"password"}},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		srcSecret, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "creds-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "creds"}, &replica); err != nil {
		t.Fatalf("replica not found: %v", err)
	}
	if _, ok := replica.Data["password"]; ok {
		t.Error("expected 'password' key to be excluded")
	}
	if string(replica.Data["username"]) != "admin" {
		t.Errorf("expected username='admin', got %q", replica.Data["username"])
	}
	if string(replica.Data["host"]) != "db.internal" {
		t.Errorf("expected host='db.internal', got %q", replica.Data["host"])
	}
}
