package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var ReplicationsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "spillway",
		Name:      "replications_total",
		Help:      "Total replication operations by spillway.",
	},
	[]string{"kind", "result"}, // result: "success" | "error"
)

var ReconcileChangesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "spillway",
		Name:      "reconcile_changes_total",
		Help:      "Total object changes applied during reconcile.",
	},
	[]string{"kind", "action"}, // action: "create" | "update" | "delete"
)

var CleanupDeletesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "spillway",
		Name:      "cleanup_deletes_total",
		Help:      "Total replica objects deleted during cleanup.",
	},
	[]string{"kind"},
)

var ReplicaRemapFailuresTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "spillway",
		Name:      "replica_remap_failures_total",
		Help:      "Total failures remapping replica events back to source objects.",
	},
	[]string{"kind", "reason"}, // reason: "nil_object" | "malformed_source_from"
)

func init() {
	metrics.Registry.MustRegister(ReplicationsTotal)
	metrics.Registry.MustRegister(ReconcileChangesTotal)
	metrics.Registry.MustRegister(CleanupDeletesTotal)
	metrics.Registry.MustRegister(ReplicaRemapFailuresTotal)
}
