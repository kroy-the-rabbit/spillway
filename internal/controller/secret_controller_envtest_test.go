package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestSecretEnvtest_ReplicaDeleteRecreatesViaWatchPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv := &envtest.Environment{}
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
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := (&SecretReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Log:              ctrl.Log.WithName("test").WithName("secret"),
		SelfHealInterval: 0, // prove delete watch/remap pipeline, not timer fallback
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setup secret reconciler: %v", err)
	}

	mgrErrCh := make(chan error, 1)
	go func() {
		if runErr := mgr.Start(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			mgrErrCh <- runErr
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache failed to sync")
	}

	k8sClient := mgr.GetClient()

	mustCreateNamespace(t, ctx, k8sClient, "platform")
	mustCreateNamespace(t, ctx, k8sClient, "team-a")

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-api-token",
			Namespace: "platform",
			Annotations: map[string]string{
				AnnotationReplicateTo: "team-a",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte("abc")},
	}
	if err := k8sClient.Create(ctx, src); err != nil {
		t.Fatalf("create source secret: %v", err)
	}

	replicaKey := types.NamespacedName{Namespace: "team-a", Name: "shared-api-token"}

	var firstReplica corev1.Secret
	waitFor(t, 10*time.Second, 100*time.Millisecond, func() (bool, error) {
		select {
		case runErr := <-mgrErrCh:
			return false, fmt.Errorf("manager exited: %w", runErr)
		default:
		}
		if err := k8sClient.Get(ctx, replicaKey, &firstReplica); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return firstReplica.UID != "", nil
	}, "initial replica creation")

	firstUID := firstReplica.UID

	if err := k8sClient.Delete(ctx, &firstReplica); err != nil {
		t.Fatalf("delete replica: %v", err)
	}

	waitFor(t, 10*time.Second, 100*time.Millisecond, func() (bool, error) {
		select {
		case runErr := <-mgrErrCh:
			return false, fmt.Errorf("manager exited: %w", runErr)
		default:
		}
		var recreated corev1.Secret
		if err := k8sClient.Get(ctx, replicaKey, &recreated); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if recreated.UID == "" || recreated.UID == firstUID {
			return false, nil
		}
		if recreated.Annotations[AnnotationSourceFrom] != "Secret/platform/shared-api-token" {
			return false, fmt.Errorf("unexpected source-from annotation: %q", recreated.Annotations[AnnotationSourceFrom])
		}
		return true, nil
	}, "replica recreation after delete")
}

func mustCreateNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

func waitFor(t *testing.T, timeout, interval time.Duration, check func() (bool, error), desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check()
		if err != nil {
			t.Fatalf("%s: %v", desc, err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", desc)
		}
		time.Sleep(interval)
	}
}

func envtestUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "fork/exec") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "KUBEBUILDER_ASSETS")
}
