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

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
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

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
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

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
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

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
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

func TestConfigMapReconcileSmoke_CleanupHandlesManagedReplicaWithoutLabel(t *testing.T) {
	ctx := context.Background()
	unlabeledReplica := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "team-b",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationSourceFrom: "ConfigMap/platform/shared-config",
			},
		},
	}
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
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
		unlabeledReplica,
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var deleted corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-b", Name: "shared-config"}, &deleted); !apierrors.IsNotFound(err) {
		t.Fatalf("expected unlabeled managed replica to be deleted, got err=%v", err)
	}
}

func TestConfigMapReconcileSmoke_InvalidMatchingSelectorDoesNotError(t *testing.T) {
	ctx := context.Background()
	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateToMatching: "!!!invalid!!!",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("expected invalid selector to be handled without reconcile error, got: %v", err)
	}
}

// TestConfigMapReconcileSmoke_TTLExpiry verifies that when an existing replica's
// expires-at annotation is in the past, the controller marks the namespace as
// permanently expired and removes the replica — not recreating it.
func TestConfigMapReconcileSmoke_TTLExpiry(t *testing.T) {
	ctx := context.Background()

	expiredTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	expiredReplica := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "team-a",
			Annotations: map[string]string{
				AnnotationManagedBy:  ManagedByValue,
				AnnotationSourceFrom: "ConfigMap/platform/app-config",
				AnnotationExpiresAt:  expiredTime,
			},
			Labels: map[string]string{LabelManagedBy: ManagedByValue},
		},
	}
	src := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "platform",
			Annotations: map[string]string{
				AnnotationReplicateTo: "team-a",
				AnnotationReplicaTTL:  "24h",
			},
		},
		Data: map[string]string{"key": "value"},
	}

	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		src,
		expiredReplica,
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "app-config"}}

	// First reconcile: detects expiry, removes replica, records team-a as expired.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var replica corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app-config"}, &replica); !apierrors.IsNotFound(err) {
		t.Fatalf("expected expired replica to be deleted, got err=%v", err)
	}

	var updatedSrc corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "app-config"}, &updatedSrc); err != nil {
		t.Fatalf("get source: %v", err)
	}
	if updatedSrc.Annotations[AnnotationExpiredNamespaces] == "" {
		t.Fatal("expected expired-namespaces annotation to be set on source")
	}

	// Second reconcile: team-a is in expired set, so no replica is created.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app-config"}, &replica); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no recreation of expired replica, got err=%v", err)
	}
}

// TestConfigMapReconcileSmoke_TTLRemovalClearsExpiredNamespaces verifies that
// removing the replica-ttl annotation causes the controller to clear the
// expired-namespaces annotation and requeue so replication resumes.
func TestConfigMapReconcileSmoke_TTLRemovalClearsExpiredNamespaces(t *testing.T) {
	ctx := context.Background()

	src := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "platform",
			Annotations: map[string]string{
				AnnotationReplicateTo:       "team-a",
				AnnotationExpiredNamespaces: "team-a",
				// No AnnotationReplicaTTL — TTL was removed.
			},
		},
		Data: map[string]string{"key": "value"},
	}

	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		src,
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "app-config"}}

	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !result.Requeue {
		t.Fatal("expected Requeue=true after clearing expired-namespaces")
	}

	var updatedSrc corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "app-config"}, &updatedSrc); err != nil {
		t.Fatalf("get source: %v", err)
	}
	if ann := updatedSrc.Annotations[AnnotationExpiredNamespaces]; ann != "" {
		t.Fatalf("expected expired-namespaces to be cleared, got %q", ann)
	}

	// Second reconcile: replica should now be created.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var replica corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app-config"}, &replica); err != nil {
		t.Fatalf("expected replica to be created after TTL removal: %v", err)
	}
}

// TestConfigMapReconcileSmoke_OwnershipConflict verifies that a pre-existing
// unmanaged configmap blocks replication without force-adopt.
func TestConfigMapReconcileSmoke_OwnershipConflict(t *testing.T) {
	ctx := context.Background()

	// The UID must be non-empty so ensureManagedOwnership sees it as an existing object.
	unmanagedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "team-a",
			UID:       "pre-existing-uid",
		},
		Data: map[string]string{"key": "original"},
	}

	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
					// No force-adopt.
				},
			},
			Data: map[string]string{"key": "new-value"},
		},
		unmanagedCM,
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var existing corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-config"}, &existing); err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if existing.Data["key"] != "original" {
		t.Fatalf("expected unmanaged configmap to be preserved (key=original), got %q", existing.Data["key"])
	}
	if existing.Annotations[AnnotationManagedBy] == ManagedByValue {
		t.Fatal("expected unmanaged configmap to remain unmanaged without force-adopt")
	}
}

// TestConfigMapReconcileSmoke_ForceAdopt verifies that with force-adopt, a
// pre-existing unmanaged configmap IS overwritten by the replica.
func TestConfigMapReconcileSmoke_ForceAdopt(t *testing.T) {
	ctx := context.Background()

	// The UID must be non-empty so ensureManagedOwnership sees it as an existing object.
	unmanagedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "team-a",
			UID:       "pre-existing-uid",
		},
		Data: map[string]string{"key": "original"},
	}

	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a",
					AnnotationForceAdopt:  "true",
				},
			},
			Data: map[string]string{"key": "new-value"},
		},
		unmanagedCM,
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var adopted corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-config"}, &adopted); err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if adopted.Data["key"] != "new-value" {
		t.Fatalf("expected force-adopt to overwrite unmanaged configmap, got key=%q", adopted.Data["key"])
	}
	if adopted.Annotations[AnnotationManagedBy] != ManagedByValue {
		t.Fatal("expected adopted configmap to have managed-by annotation")
	}
}

// TestConfigMapReconcileSmoke_NamespaceConsent verifies that namespace accept-from
// annotation correctly gates replication.
//
// team-a has accept-from: "ConfigMap/platform/*" → should receive replica.
// team-b has accept-from: "ConfigMap/other/*"    → should NOT receive replica.
func TestConfigMapReconcileSmoke_NamespaceConsent(t *testing.T) {
	ctx := context.Background()

	c, scheme := newCMSmokeClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "team-a",
				Annotations: map[string]string{
					AnnotationAcceptFrom: "ConfigMap/platform/*",
				},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "team-b",
				Annotations: map[string]string{
					AnnotationAcceptFrom: "ConfigMap/other/*",
				},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "platform",
				Annotations: map[string]string{
					AnnotationReplicateTo: "team-a,team-b",
				},
			},
			Data: map[string]string{"key": "value"},
		},
	)

	r := &ConfigMapReconciler{Client: c, Scheme: scheme, Log: log.Log.WithName("test"), Recorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "shared-config"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// team-a accepts from ConfigMap/platform/* — should have replica.
	var teamAReplica corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "shared-config"}, &teamAReplica); err != nil {
		t.Fatalf("expected replica in team-a (accept-from matches): %v", err)
	}

	// team-b accepts from ConfigMap/other/* — should NOT have replica.
	var teamBReplica corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "team-b", Name: "shared-config"}, &teamBReplica); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no replica in team-b (accept-from rejects platform source), got err=%v", err)
	}
}

func newCMSmokeClient(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.ConfigMap{}, configMapReplicaSourceFieldIdx, func(obj client.Object) []string {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok {
				return nil
			}
			return replicaSourceFieldIndexValue(cm)
		}).
		WithObjects(objs...).
		Build(), scheme
}
