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

func init() {
	metrics.Registry.MustRegister(ReplicationsTotal)
}
