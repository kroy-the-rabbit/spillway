# Spillway Enterprise Security / SOC 2 Plan

## Goal

Reduce the security and compliance gaps most likely to block enterprise adoption of spillway, with priority on tenant isolation, auditability, deployment hardening, and supply-chain integrity.

## Principles

- Prefer secure defaults over operator-side compensating controls.
- Make authorization boundaries explicit and enforceable in product behavior.
- Produce durable evidence for sensitive actions, not just best-effort logs.
- Ensure published security claims match the implemented controls.

## Priority 1: Authorization Model

### Objective

Prevent untrusted tenants or namespace owners from using spillway to copy, overwrite, or delete data across namespace boundaries without explicit authorization.

### Work

- Change namespace consent from allow-by-default to deny-by-default.
- Add a configurable protected namespace denylist, not just `kube-system`.
- Add a chart value and controller enforcement for protected namespaces.
- Restrict or disable `spillway.kroy.io/force-adopt` by default.
- Ship policy examples for Kyverno / Gatekeeper to limit:
  - who may create `SpillwayProfile` objects
  - who may set replication annotations
  - who may target wildcard namespaces
  - who may use `force-adopt`

### Acceptance Criteria

- A namespace without explicit consent cannot receive replicated objects.
- Sensitive namespaces are blocked unless explicitly allowed by configuration.
- `force-adopt` requires explicit operator approval path.
- Documentation includes deployable admission-policy examples.

## Priority 2: Auditability and Evidence

### Objective

Make replication activity defensible in a SOC 2 review by producing durable, queryable evidence for high-risk actions.

### Work

- Add structured logs for every create, update, delete, skip, and conflict decision.
- Log source kind, source namespace/name, target namespace, replication mode, and reason.
- Emit explicit records when `force-adopt` overwrites an existing object.
- Emit explicit records for cleanup deletes and TTL expiry behavior.
- Document log retention and example queries for incident review / evidence collection.

### Acceptance Criteria

- Every material replication action is visible in structured logs.
- Overwrites and deletions are distinguishable from routine syncs.
- Operators can reconstruct which source caused a target change.
- Documentation maps records to audit and incident-response use cases.

## Priority 3: Replication Semantics Hardening

### Objective

Reduce unintended side effects from copying metadata across namespaces.

### Work

- Replace blanket non-spillway annotation copying with an allowlist or opt-in copy model.
- Identify and exclude annotations commonly used by policy engines, injectors, or workload controllers.
- Document compatibility impact and migration guidance.
- Add tests for annotation copy behavior and excluded metadata classes.

### Acceptance Criteria

- Replicas do not inherit unrelated controller or policy annotations by default.
- Metadata propagation is explicit and documented.
- Tests cover both allowed and denied annotation behavior.

## Priority 4: Deployment Hardening

### Objective

Provide enterprise-safe deployment defaults that minimize exposure without requiring custom platform work.

### Work

- Make metrics exposure opt-in or more restrictive by default.
- Add optional egress NetworkPolicy support in the chart.
- Ship hardened Helm values for isolated enterprise clusters.
- Document recommended production settings for:
  - metrics access
  - namespace protections
  - admission policy integration
  - controller replica placement and availability

### Acceptance Criteria

- Default deployment minimizes unnecessary service exposure.
- Chart supports both ingress and egress policy controls.
- Production guidance exists as a ready-to-apply values file.

## Priority 5: Supply-Chain Integrity

### Objective

Ensure release and artifact claims meet enterprise expectations and are verifiably true.

### Work

- Fix the mismatch between security documentation and release automation.
- Either implement image signing, SBOM generation, and provenance attestation, or remove the claim until implemented.
- Add release verification steps that fail if required security artifacts are missing.
- Document how operators verify released images and charts.

### Acceptance Criteria

- Security docs do not claim controls that the pipeline does not implement.
- Releases include the expected integrity artifacts, or docs explicitly state they do not.
- Release automation enforces the expected artifact set.

## Priority 6: Validation and Governance

### Objective

Prevent regressions in the security model and keep documentation aligned with behavior.

### Work

- Add tests for deny-by-default consent.
- Add tests for protected namespace denial and explicit override behavior.
- Add tests for `force-adopt` restrictions.
- Add checks that security documentation matches implemented release controls.
- Review CRD and annotation validation to reject unsafe or ambiguous inputs where possible.

### Acceptance Criteria

- CI fails when core security expectations regress.
- Unsafe defaults cannot be reintroduced silently.
- Security-sensitive behavior is covered by automated tests.

## Suggested Execution Order

1. Authorization model changes
2. Documentation correction for release/security claims
3. Audit logging and evidence improvements
4. Deployment hardening changes
5. Annotation propagation redesign
6. Supply-chain implementation and release gates
7. Expanded tests and policy examples

## Done Definition

spillway is materially closer to enterprise-ready when:

- cross-namespace replication requires explicit authorization
- sensitive namespaces are protected by default
- overwrite and delete paths are auditable
- deployment defaults are conservative
- published security claims are true and verifiable
- CI enforces the intended security model
