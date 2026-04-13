package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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

func TestProfileReconcileSmoke_ConditionsSetOnSuccess(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "platform"},
		Data:       map[string][]byte{"token": []byte("s3cr3t")},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-profile", Namespace: "platform"},
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
		srcSecret, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "cond-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var updated spillwayv1alpha1.SpillwayProfile
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "platform", Name: "cond-profile"}, &updated); err != nil {
		t.Fatalf("get profile: %v", err)
	}

	readyCond := apimeta.FindStatusCondition(updated.Status.Conditions, ProfileConditionReady)
	if readyCond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True, got %v (reason=%s)", readyCond.Status, readyCond.Reason)
	}

	saCond := apimeta.FindStatusCondition(updated.Status.Conditions, ProfileConditionSourcesAvailable)
	if saCond == nil {
		t.Fatal("expected SourcesAvailable condition to be set")
	}
	if saCond.Status != metav1.ConditionTrue {
		t.Errorf("expected SourcesAvailable=True, got %v (reason=%s)", saCond.Status, saCond.Reason)
	}
}

func TestProfileReconcileSmoke_TLSProjectionDowngradesReplicaTypeWhenKeyMissing(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-cert", Namespace: "platform"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "shared-cert", IncludeKeys: []string{corev1.TLSCertKey}},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		srcSecret, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "tls-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "shared-cert"}, &replica); err != nil {
		t.Fatalf("replica not found: %v", err)
	}
	if replica.Type != corev1.SecretTypeOpaque {
		t.Fatalf("expected Opaque replica type for partial TLS projection, got %q", replica.Type)
	}
	if _, ok := replica.Data[corev1.TLSPrivateKeyKey]; ok {
		t.Fatal("expected tls.key to be omitted by includeKeys")
	}
}

func TestProfileReconcileSmoke_TLSProjectionPreservesTLSTypeWhenKeysRemainComplete(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-cert", Namespace: "platform"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "shared-cert", IncludeKeys: []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey}},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		srcSecret, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "tls-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var replica corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "shared-cert"}, &replica); err != nil {
		t.Fatalf("replica not found: %v", err)
	}
	if replica.Type != corev1.SecretTypeTLS {
		t.Fatalf("expected TLS replica type when both TLS keys are projected, got %q", replica.Type)
	}
}

func TestProfileReconcileSmoke_ConditionsSetOnMissingSource(t *testing.T) {
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cond-profile2", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "missing-secret"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "cond-profile2")); err != nil {
		t.Fatalf("reconcile should not error on missing source, got: %v", err)
	}

	var updated spillwayv1alpha1.SpillwayProfile
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "platform", Name: "cond-profile2"}, &updated); err != nil {
		t.Fatalf("get profile: %v", err)
	}

	// Ready should be True (no sync error, just source missing).
	readyCond := apimeta.FindStatusCondition(updated.Status.Conditions, ProfileConditionReady)
	if readyCond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True when source simply missing (no error), got %v", readyCond.Status)
	}

	// SourcesAvailable should be False.
	saCond := apimeta.FindStatusCondition(updated.Status.Conditions, ProfileConditionSourcesAvailable)
	if saCond == nil {
		t.Fatal("expected SourcesAvailable condition to be set")
	}
	if saCond.Status != metav1.ConditionFalse {
		t.Errorf("expected SourcesAvailable=False when source is missing, got %v", saCond.Status)
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

func TestProfileReconcileSmoke_DoesNotAdoptUnmanagedSecret(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "platform"},
		Data:       map[string][]byte{"token": []byte("new-value")},
	}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "team-a"},
		Data:       map[string][]byte{"token": []byte("keep-me")},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "my-profile", Namespace: "platform"},
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
		srcSecret, existing, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "my-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var current corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "platform-token"}, &current); err != nil {
		t.Fatalf("get target: %v", err)
	}
	if got := string(current.Data["token"]); got != "keep-me" {
		t.Fatalf("expected unmanaged secret to be preserved, got %q", got)
	}
	if current.Annotations[AnnotationProfileRef] != "" {
		t.Fatalf("expected unmanaged secret to remain unowned, got profile-ref=%q", current.Annotations[AnnotationProfileRef])
	}
}

func TestProfileReconcileSmoke_DoesNotOverwriteAnnotationManagedReplica(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "platform"},
		Data:       map[string][]byte{"token": []byte("new-value")},
	}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-token",
			Namespace: "team-a",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationSourceFrom: "Secret/platform/annotation-source",
			},
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
			},
		},
		Data: map[string][]byte{"token": []byte("annotation-owned")},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "my-profile", Namespace: "platform"},
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
		srcSecret, existing, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "my-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var current corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "platform-token"}, &current); err != nil {
		t.Fatalf("get target: %v", err)
	}
	if got := string(current.Data["token"]); got != "annotation-owned" {
		t.Fatalf("expected annotation-managed replica to be preserved, got %q", got)
	}
	if current.Annotations[AnnotationProfileRef] != "" {
		t.Fatalf("expected annotation-managed replica to stay annotation-managed, got profile-ref=%q", current.Annotations[AnnotationProfileRef])
	}
}

func TestProfileReconcileSmoke_EnforcesConsentPerSource(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "platform"},
		Data:       map[string][]byte{"token": []byte("s3cr3t")},
	}
	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "platform"},
		Data:       map[string]string{"host": "db.internal"},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "mixed-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "shared-secret"},
				{Kind: "ConfigMap", Name: "shared-config"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "team-a",
				Annotations: map[string]string{
					AnnotationAcceptFrom: "ConfigMap/platform/shared-config",
				},
			},
		},
		srcSecret, srcCM, profile,
	)
	r := newProfileRec(t, c)

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "mixed-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	var cmReplica corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "shared-config"}, &cmReplica); err != nil {
		t.Fatalf("expected consented configmap replica: %v", err)
	}

	var secretReplica corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "shared-secret"}, &secretReplica); err == nil {
		t.Fatal("expected secret replica to be denied by accept-from")
	}
}
