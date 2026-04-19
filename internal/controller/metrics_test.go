package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	spillwayv1alpha1 "spillway/api/v1alpha1"
)

func resetMetricsForTest() {
	ReplicationsTotal.Reset()
	ReplicationOutcomesTotal.Reset()
	ReconcileChangesTotal.Reset()
	CleanupDeletesTotal.Reset()
	ReplicaRemapFailuresTotal.Reset()
}

func TestRecordReplicationOutcomeCompatibilityMetrics(t *testing.T) {
	resetMetricsForTest()

	recordReplicationOutcome("Secret", modeAnnotation, "success", 2)
	recordReplicationOutcome("Secret", modeAnnotation, "error", 3)
	recordReplicationOutcome("Secret", modeAnnotation, "conflict", 4)

	if got := testutil.ToFloat64(ReplicationOutcomesTotal.WithLabelValues("Secret", modeAnnotation, "success")); got != 2 {
		t.Fatalf("success outcomes = %v, want 2", got)
	}
	if got := testutil.ToFloat64(ReplicationOutcomesTotal.WithLabelValues("Secret", modeAnnotation, "error")); got != 3 {
		t.Fatalf("error outcomes = %v, want 3", got)
	}
	if got := testutil.ToFloat64(ReplicationOutcomesTotal.WithLabelValues("Secret", modeAnnotation, "conflict")); got != 4 {
		t.Fatalf("conflict outcomes = %v, want 4", got)
	}
	if got := testutil.ToFloat64(ReplicationsTotal.WithLabelValues("Secret", "success")); got != 2 {
		t.Fatalf("compat success metric = %v, want 2", got)
	}
	if got := testutil.ToFloat64(ReplicationsTotal.WithLabelValues("Secret", "error")); got != 3 {
		t.Fatalf("compat error metric = %v, want 3", got)
	}
}

func TestProfileReconcileRecordsConsentDeniedOutcome(t *testing.T) {
	resetMetricsForTest()

	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-token", Namespace: "platform"},
		Data:       map[string][]byte{"token": []byte("s3cr3t")},
	}
	targetNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "team-a",
			Annotations: map[string]string{AnnotationAcceptFrom: "ConfigMap/platform/*"},
		},
	}
	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-profile", Namespace: "platform"},
		Spec: spillwayv1alpha1.SpillwayProfileSpec{
			TargetNamespaces: []string{"team-a"},
			Sources: []spillwayv1alpha1.ProfileSource{
				{Kind: "Secret", Name: "platform-token"},
			},
		},
	}

	c := newProfileClient(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}},
		targetNS,
		srcSecret, profile,
	)
	r := newProfileRec(t, c)
	r.Opts.RequireNamespaceConsent = true

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "deny-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	if got := testutil.ToFloat64(ReplicationOutcomesTotal.WithLabelValues("Secret", modeProfile, "consent_denied")); got != 1 {
		t.Fatalf("consent denied outcomes = %v, want 1", got)
	}

	var replica corev1.Secret
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "platform-token"}, &replica)
	if err == nil {
		t.Fatal("expected no replica to be created when consent is denied")
	}
}

func TestProfileReconcileRecordsMissingSourceOutcome(t *testing.T) {
	resetMetricsForTest()

	profile := &spillwayv1alpha1.SpillwayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-profile", Namespace: "platform"},
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

	if _, err := r.Reconcile(context.Background(), profileReq("platform", "missing-profile")); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	if got := testutil.ToFloat64(ReplicationOutcomesTotal.WithLabelValues("Secret", modeProfile, "source_missing")); got != 1 {
		t.Fatalf("source missing outcomes = %v, want 1", got)
	}
}
