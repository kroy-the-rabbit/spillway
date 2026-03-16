package controller

import "github.com/go-logr/logr"

// Audit action constants used in structured log entries. Every material
// replication action must be recorded using one of these values so that
// operators can reconstruct what happened from logs alone.
const (
	auditCreate      = "create"
	auditUpdate      = "update"
	auditDelete      = "delete"
	auditSkip        = "skip"
	auditConflict    = "conflict"
	auditAdopt       = "adopt"
	auditExpire      = "ttl_expire"
	auditCleanup     = "cleanup"
	auditConsentDeny = "consent_denied"
	auditForceDeny   = "force_adopt_disabled"
)

// Replication mode constants identify whether an action was triggered by a
// source annotation or a SpillwayProfile.
const (
	modeAnnotation = "annotation"
	modeProfile    = "profile"
)

// auditLog emits a structured log entry for a single replication event.
//
// Log fields:
//
//	action          — what happened (create, update, delete, skip, conflict, …)
//	mode            — "annotation" or "profile"
//	sourceKind      — Secret or ConfigMap
//	sourceNamespace — namespace of the source object
//	sourceName      — name of the source object
//	targetNamespace — namespace where the replica is (or would be) written
//	reason          — human-readable explanation for the action
//
// These entries are the primary audit trail for replication activity and
// should map directly to incident-response queries and SOC 2 evidence.
func auditLog(
	log logr.Logger,
	action, mode, srcKind, srcNamespace, srcName, targetNamespace, reason string,
) {
	log.Info("replication audit",
		"action", action,
		"mode", mode,
		"sourceKind", srcKind,
		"sourceNamespace", srcNamespace,
		"sourceName", srcName,
		"targetNamespace", targetNamespace,
		"reason", reason,
	)
}
