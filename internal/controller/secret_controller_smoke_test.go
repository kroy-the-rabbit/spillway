package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestSecretReconcileSmoke_AllRespectsDefaultExcludes(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "all",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-api-token"}, &replicated); err != nil {
		t.Fatalf("get replicated secret: %v", err)
	}
	if replicated.Annotations[AnnotationManagedBy] != ManagedByValue {
		t.Fatalf("expected managed-by annotation, got %q", replicated.Annotations[AnnotationManagedBy])
	}
	if replicated.Annotations[AnnotationSourceFrom] != "Secret/platform/shared-api-token" {
		t.Fatalf("unexpected source-from annotation: %q", replicated.Annotations[AnnotationSourceFrom])
	}

	var kubeSystemCopy corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "shared-api-token"}, &kubeSystemCopy)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no kube-system replica, got err=%v", err)
	}
}

func TestSecretReconcileSmoke_ExplicitKubeSystemIncludeOverridesDefault(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "kube-*,kube-system",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "shared-api-token"}, &replicated); err != nil {
		t.Fatalf("expected kube-system replica from explicit include override: %v", err)
	}
}

func TestSecretReconcileSmoke_LabelSelectorTargeting(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "team-infra",
			Labels: map[string]string{"team": "platform"},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-other"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateToMatching: "team=platform",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-infra", Name: "shared-api-token"}, &replicated); err != nil {
		t.Fatalf("expected replica in team-infra: %v", err)
	}

	var notReplicated corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: "team-other", Name: "shared-api-token"}, &notReplicated)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no replica in team-other, got err=%v", err)
	}
}

func TestSecretReconcileSmoke_ManagedByLabelIsSet(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var replicated corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-api-token"}, &replicated); err != nil {
		t.Fatalf("get replicated secret: %v", err)
	}
	if replicated.Labels[LabelManagedBy] != ManagedByValue {
		t.Fatalf("expected managed-by label %q=%q, got %q", LabelManagedBy, ManagedByValue, replicated.Labels[LabelManagedBy])
	}
}

func TestSecretReconcileSmoke_CleanupHandlesManagedReplicaWithoutLabel(t *testing.T) {
	ctx := context.Background()
	// Pre-create a replica WITHOUT the managed-by label; cleanup should still find it
	// via source index + annotation and delete it.
	unlabeledReplica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-api-token",
			Namespace: "team-b",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationSourceFrom: "Secret/platform/shared-api-token",
			},
			// No Labels — won't appear in label-filtered List
		},
	}
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
		unlabeledReplica,
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var deleted corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-b", Name: "shared-api-token"}, &deleted); !apierrors.IsNotFound(err) {
		t.Fatalf("expected unlabeled managed replica to be deleted, got err=%v", err)
	}
}

func TestSecretReconcileSmoke_InvalidMatchingSelectorDoesNotError(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateToMatching: "!!!invalid!!!",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}); err != nil {
		t.Fatalf("expected invalid selector to be handled without reconcile error, got: %v", err)
	}
}

func TestSecretReconcileSmoke_DeletedReplicaIsRecreated(t *testing.T) {
	ctx := context.Background()
	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-api-token",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte("abc")},
		},
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-api-token"}}

	// Initial reconcile — creates replica.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	var replica corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-api-token"}, &replica); err != nil {
		t.Fatalf("expected replica after first reconcile: %v", err)
	}

	// Simulate kubectl delete on the replica.
	if err := c.Delete(ctx, &replica); err != nil {
		t.Fatalf("delete replica: %v", err)
	}

	// The watch mapper re-enqueues the source; simulate by reconciling the source again.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile after replica deletion: %v", err)
	}
	var recreated corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-api-token"}, &recreated); err != nil {
		t.Fatalf("expected replica to be recreated: %v", err)
	}
}

// TestSecretReconcileSmoke_TTLExpiry verifies that when an existing replica's
// expires-at annotation is in the past, the controller marks the namespace as
// permanently expired and removes the replica — not recreating it.
func TestSecretReconcileSmoke_TTLExpiry(t *testing.T) {
	ctx := context.Background()

	expiredTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	// Pre-create a replica that has already expired.
	expiredReplica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-token",
			Namespace: "team-a",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationSourceFrom: "Secret/platform/my-token",
				AnnotationExpiresAt:  expiredTime,
			},
			Labels: map[string]string{LabelManagedBy: ManagedByValue},
		},
	}
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-token",
			Namespace: "platform",
			Annotations: map[string]string{
				AnnotationReplicateTo: "team-a",
				AnnotationReplicaTTL:  "24h",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte("abc")},
	}

	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		src,
		expiredReplica,
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "my-token"}}

	// First reconcile: detects expiry, removes replica, records team-a as expired.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Replica must be gone.
	var replica corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "my-token"}, &replica); !apierrors.IsNotFound(err) {
		t.Fatalf("expected expired replica to be deleted, got err=%v", err)
	}

	// Source must have expired-namespaces annotation containing team-a.
	var updatedSrc corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "my-token"}, &updatedSrc); err != nil {
		t.Fatalf("get source: %v", err)
	}
	expiredAnn := updatedSrc.Annotations[AnnotationExpiredNamespaces]
	if expiredAnn == "" {
		t.Fatal("expected expired-namespaces annotation to be set on source")
	}

	// Second reconcile: team-a is in expired set, so no replica is created.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "my-token"}, &replica); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no recreation of expired replica, got err=%v", err)
	}
}

// TestSecretReconcileSmoke_TTLRemovalClearsExpiredNamespaces verifies that
// removing the replica-ttl annotation causes the controller to clear the
// expired-namespaces annotation and requeue so replication resumes.
func TestSecretReconcileSmoke_TTLRemovalClearsExpiredNamespaces(t *testing.T) {
	ctx := context.Background()

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-token",
			Namespace: "platform",
			Annotations: map[string]string{
				AnnotationReplicateTo:       "team-a",
				AnnotationExpiredNamespaces: "team-a",
				// No AnnotationReplicaTTL — TTL was removed.
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte("abc")},
	}

	c, scheme := newSecretSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		src,
	)

	r := &SecretReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "my-token"}}

	// Reconcile: TTL is gone, so expired-namespaces must be cleared.
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !result.Requeue {
		t.Fatal("expected Requeue=true after clearing expired-namespaces")
	}

	var updatedSrc corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "my-token"}, &updatedSrc); err != nil {
		t.Fatalf("get source: %v", err)
	}
	if ann := updatedSrc.Annotations[AnnotationExpiredNamespaces]; ann != "" {
		t.Fatalf("expected expired-namespaces to be cleared, got %q", ann)
	}

	// Second reconcile (simulating the requeue): replica should now be created.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var replica corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "my-token"}, &replica); err != nil {
		t.Fatalf("expected replica to be created after TTL removal: %v", err)
	}
}

func newSecretSmokeClient(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Secret{}, secretReplicaSourceFieldIdx, func(obj client.Object) []string {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return nil
			}
			return replicaSourceFieldIndexValue(secret)
		}).
		WithObjects(objs...).
		Build(), scheme
}
