package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/internal/util"
)

// AgentHealthState is the machine-readable routing gate persisted on
// agent.health_state (RIC-806). It is distinct from agent.status (the runtime
// liveness signal idle/working/blocked/error/offline) and from archived_at
// (soft delete): it says whether the router may ASSIGN new work to this agent.
//
// The string values are the canonical literals stored in the column and
// serialized over the wire; never reformat them. NULL on the column (only for
// rows created before migration 400) is treated as Active by every read path.
type AgentHealthState string

const (
	// HealthStateActive: eligible for automatic and human assignment.
	HealthStateActive AgentHealthState = "active"
	// HealthStateStandby: visible, held out of automatic assignment, but a
	// human may still dispatch explicitly. Used to drain or stage a model
	// without the finality of quarantine.
	HealthStateStandby AgentHealthState = "standby"
	// HealthStateQuarantined: isolated after probe failure or manual action.
	// Not eligible for automatic OR human assignment until a recovery gate
	// promotes it. The model row stays so the platform keeps reasoning about
	// it (unlike archive).
	HealthStateQuarantined AgentHealthState = "quarantined"
	// HealthStateDisabled: hard-stopped, never eligible. Use for withdrawn
	// models (e.g. gpt-5.3-codex) where archive would lose the row the
	// platform still needs.
	HealthStateDisabled AgentHealthState = "disabled"
)

// assignableStates is the set a router admits for automatic assignment.
// standby and quarantined/disabled are all excluded; the distinction
// between them is whether a human may still dispatch (standby: yes;
// quarantined/disabled: no).
var assignableStates = map[AgentHealthState]bool{
	HealthStateActive: true,
}

// IsAssignable reports whether the router may assign new work to an agent in
// this state. standby, quarantined, and disabled all return false.
func (s AgentHealthState) IsAssignable() bool { return assignableStates[s] }

// IsBlocked reports whether the state is a hard refusal: neither automatic
// nor human assignment is permitted, and the user's fix is a probe/override,
// not a machine repair or a wait. quarantined and disabled are blocked;
// active and standby are not.
func (s AgentHealthState) IsBlocked() bool {
	return s == HealthStateQuarantined || s == HealthStateDisabled
}

// normalizeHealthState maps a raw column value (which may be NULL for
// pre-migration rows) onto a canonical state. NULL and the empty string are
// Active so a never-probed agent is never gated out by default.
func normalizeHealthState(raw string) AgentHealthState {
	if raw == "" {
		return HealthStateActive
	}
	switch AgentHealthState(raw) {
	case HealthStateActive, HealthStateStandby, HealthStateQuarantined, HealthStateDisabled:
		return AgentHealthState(raw)
	default:
		// A value outside the CHECK set is impossible for rows written after
		// migration 400, but treat any stray value conservatively: quarantine
		// rather than admit, since an unknown state is safer to refuse.
		return HealthStateQuarantined
	}
}

// HealthMetadata is the structured probe / audit registry stored on
// agent.health_metadata (RIC-806). It carries the last probe result, the
// recovery counter, the required threshold, and the auditor identity for any
// manual override. The JSONB column stores this verbatim; callers round-trip
// it through (un)marshalHealthMetadata.
type HealthMetadata struct {
	// LastProbeAt is when the most recent probe ran (RFC3339). Zero/absent
	// means no probe has ever run.
	LastProbeAt string `json:"last_probe_at,omitempty"`
	// LastStatus is the probe's verdict: "ok" or "error". Absent when no
	// probe has run. Never parsed by the router — the gate reads
	// health_state, which the probe writer promotes/demotes.
	LastStatus string `json:"last_status,omitempty"`
	// LastError is the error string a failing probe recorded, for logs and
	// the durable notice. Absent on success. Never parsed for routing.
	LastError string `json:"last_error,omitempty"`
	// LastLatencyMs is the probe's round-trip latency in milliseconds, for
	// health dashboards. Absent when no probe has run.
	LastLatencyMs int64 `json:"last_latency_ms,omitempty"`
	// ConsecutiveSuccesses is the recovery counter. Incremented on each
	// successful probe; reset to 0 on any failure. A state is promoted out
	// of 'quarantined' only when this reaches RecoverySuccessThreshold.
	ConsecutiveSuccesses int `json:"consecutive_successes,omitempty"`
	// RecoverySuccessThreshold is the N consecutive successful probes
	// required before the recovery gate promotes the agent back to active.
	// Recorded so an operator can raise the bar per-agent; defaults to
	// DefaultRecoverySuccessThreshold when absent.
	RecoverySuccessThreshold int `json:"recovery_success_threshold,omitempty"`
	// AuditorID is the user who last set or overrode the health state
	// manually (UUID). Absent when the state was set by an automated probe
	// or by the migration backfill. Carried so the override surface can
	// show "set by X" and the audit log can attribute the decision.
	AuditorID string `json:"auditor_id,omitempty"`
	// AuditorNote is a free-text reason the auditor left when overriding
	// (e.g. "model withdrawn upstream, see RIC-804"). Absent for automated
	// transitions.
	AuditorNote string `json:"auditor_note,omitempty"`
	// AuditedAt is when the last manual override happened (RFC3339).
	AuditedAt string `json:"audited_at,omitempty"`
}

// DefaultRecoverySuccessThreshold is the default number of consecutive
// successful probes required to promote an agent out of 'quarantined'. A
// per-agent threshold recorded in HealthMetadata.RecoverySuccessThreshold
// overrides this.
const DefaultRecoverySuccessThreshold = 3

// ErrAgentHealthBlocked is the sentinel the enqueue paths return when the
// agent's health_state refuses assignment. It carries the verdict so the
// handler layer can render the durable notice with the auditor's reason and
// the dispatch reason code, exactly as it does for runtime-unusable. Use
// errors.Is to detect it; never string-match.
type ErrAgentHealthBlocked struct {
	State AgentHealthState
	Agent db.Agent
}

func (e *ErrAgentHealthBlocked) Error() string {
	return fmt.Sprintf("agent health_state is %s: not eligible for assignment", e.State)
}

// healthBlocked returns an *ErrAgentHealthBlocked for the agent if its
// health_state refuses assignment, or nil otherwise. Used by the enqueue
// paths so a quarantined/disabled agent is never handed a new task even when
// the runtime is online and the trigger would otherwise queue.
func healthBlocked(agent db.Agent) error {
	state := normalizeHealthState(agent.HealthState.String)
	if !state.IsBlocked() {
		return nil
	}
	return &ErrAgentHealthBlocked{State: state, Agent: agent}
}

// textHealthState wraps an AgentHealthState in a valid pgtype.Text for the
// sqlc-generated UpdateAgentHealthStateParams. Centralized so the wrap does
// not drift across the write paths.
func textHealthState(s AgentHealthState) pgtype.Text {
	return pgtype.Text{String: string(s), Valid: true}
}


// marshalHealthMetadata serializes the registry to JSONB bytes. nil is
// preserved so a caller can pass HealthMetadata{} to clear.
func marshalHealthMetadata(m HealthMetadata) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		// HealthMetadata is a struct of plain scalars; marshal cannot fail
		// for it. If it ever does, persist nothing rather than corrupt the
		// column.
		return nil
	}
	return b
}

// unmarshalHealthMetadata reads the registry off JSONB bytes. An empty/nil
// column or any parse failure yields an empty HealthMetadata — never a
// different routing decision, matching the runtime_offline_reason contract.
func unmarshalHealthMetadata(raw []byte) HealthMetadata {
	if len(raw) == 0 {
		return HealthMetadata{}
	}
	var m HealthMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return HealthMetadata{}
	}
	return m
}

// AgentHealthGate is the readiness half that depends on the agent's
// machine-readable health_state (RIC-806), split out from AgentReadiness so
// every branch is testable without a database. It returns a Blocked verdict
// (with ReasonAgentQuarantined) for quarantined/disabled agents; every other
// state defers to the runtime verdict so the existing readiness logic stays
// authoritative for the machine-side decision.
//
// Callers: AgentReadiness composes this with the runtime verdict. The
// enqueue path calls AgentReadiness; no separate call is needed.
func AgentHealthGate(agent db.Agent) AgentVerdict {
	state := normalizeHealthState(agent.HealthState.String)
	if !state.IsBlocked() {
		return AgentVerdict{} // zero-value: Availability == AgentAvailable
	}
	meta := unmarshalHealthMetadata(agent.HealthMetadata)
	detail := fmt.Sprintf("agent health_state is %s", state)
	if meta.AuditorNote != "" {
		detail = fmt.Sprintf("agent health_state is %s: %s", state, meta.AuditorNote)
	}
	return AgentVerdict{
		Availability: AgentBlocked,
		Reason:       dispatch.ReasonAgentQuarantined,
		Detail:       detail,
	}
}

// AgentReadinessWithHealth extends the pre-RIC-806 AgentReadiness with the
// health_state gate. It is the single source of truth shared by the enqueue
// path and every admission surface: the health gate runs first (a quarantined
// agent is blocked regardless of its runtime), then the archived / runtime
// checks run unchanged.
//
// This function is the canonical entry point for routing decisions. The
// legacy AgentReadiness is preserved for any path that pre-dates the health
// column; new code calls this.
func AgentReadinessWithHealth(ctx context.Context, lookup RuntimeLookup, agent db.Agent) (AgentVerdict, error) {
	if v := AgentHealthGate(agent); v.Blocked() {
		return v, nil
	}
	return AgentReadiness(ctx, lookup, agent)
}

// RecordProbeResult writes a probe result to agent.health_metadata and, when
// the result is a failure, degrades the agent to 'quarantined'. On success,
// the consecutive-success counter is incremented and, if it reaches the
// threshold while the agent is quarantined, the recovery gate promotes it
// back to 'active'. The state column is updated atomically with the metadata.
//
// An automatic transition NEVER changes an issue's status — the routing gate
// is about the agent row, not any issue it was assigned. Recovery requires a
// human's explicit promotion only insofar as the auditor is recorded for a
// manual override; the automated recovery path records "auto" in
// auditor_id's place.
func (s *TaskService) RecordProbeResult(ctx context.Context, agentID pgtype.UUID, success bool, errMsg string, latencyMs int64) (db.Agent, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("load agent for health probe: %w", err)
	}
	meta := unmarshalHealthMetadata(agent.HealthMetadata)
	meta.LastProbeAt = time.Now().UTC().Format(time.RFC3339Nano)
	meta.LastLatencyMs = latencyMs

	if success {
		meta.LastStatus = "ok"
		meta.LastError = ""
		meta.ConsecutiveSuccesses++
		threshold := meta.RecoverySuccessThreshold
		if threshold <= 0 {
			threshold = DefaultRecoverySuccessThreshold
		}
		// Recovery gate: promote out of 'quarantined' only after N consecutive
		// successes. 'disabled' is a human-only state — an automated probe
		// never promotes it, matching "disabled = withdrawn model".
		state := normalizeHealthState(agent.HealthState.String)
		if state == HealthStateQuarantined && meta.ConsecutiveSuccesses >= threshold {
			meta.AuditorID = "auto"
			meta.AuditorNote = "automated recovery: consecutive probe threshold reached"
			meta.AuditedAt = meta.LastProbeAt
			return s.Queries.UpdateAgentHealthState(ctx, db.UpdateAgentHealthStateParams{
				ID:             agentID,
				HealthState:    textHealthState(HealthStateActive),
				HealthMetadata: marshalHealthMetadata(meta),
			})
		}
	} else {
		meta.LastStatus = "error"
		meta.LastError = errMsg
		meta.ConsecutiveSuccesses = 0
		// Degrade: any failure of an assignable agent quarantines it. A probe
		// cannot 'disable' — that's a human-only terminal state. A probe of an
		// already-quarantined agent only refreshes the metadata + resets the
		// counter (already 0).
		state := normalizeHealthState(agent.HealthState.String)
		if state == HealthStateActive || state == HealthStateStandby {
			meta.AuditorID = "auto"
			meta.AuditorNote = "automated degradation: probe failed"
			meta.AuditedAt = meta.LastProbeAt
			return s.Queries.UpdateAgentHealthState(ctx, db.UpdateAgentHealthStateParams{
				ID:             agentID,
				HealthState:    textHealthState(HealthStateQuarantined),
				HealthMetadata: marshalHealthMetadata(meta),
			})
		}
	}

	// No state transition: persist the refreshed metadata alone. Passing the
	// unchanged state keeps the COALESCE-free UpdateAgentHealthState atomic;
	// a concurrent override between the read and the write is still safe
	// because the override writer uses the same query on the same row.
	unchanged := normalizeHealthState(agent.HealthState.String)
	return s.Queries.UpdateAgentHealthState(ctx, db.UpdateAgentHealthStateParams{
		ID:             agentID,
		HealthState:    textHealthState(unchanged),
		HealthMetadata: marshalHealthMetadata(meta),
	})
}

// recoveryGateSatisfied is the pure decision the recovery gate makes from a
// state, a consecutive-success counter, a threshold, and an auditor note.
// It is extracted from PromoteAgentHealth so the invariant has one canonical
// home that a DB-free test can fix: "recovery requires N consecutive successful
// probes for a quarantined agent; a disabled agent needs a human override with
// a non-empty note". active/standby are idempotent no-ops. The threshold
// argument may be 0, in which case DefaultRecoverySuccessThreshold applies.
//
// PromoteAgentHealth calls this after loading the row; keeping the decision
// here means the routing matrix and the service path cannot drift apart.
func recoveryGateSatisfied(state AgentHealthState, successes, threshold int, note string) bool {
	if threshold <= 0 {
		threshold = DefaultRecoverySuccessThreshold
	}
	switch state {
	case HealthStateQuarantined:
		return successes >= threshold
	case HealthStateDisabled:
		// A human override must pass a non-empty note; the note is the audit
		// record acceptance criterion 4 + 6 require.
		return note != ""
	case HealthStateActive, HealthStateStandby:
		return true
	default:
		return false
	}
}

// PromoteAgentHealth is the recovery gate: a human calls this to move an
// agent out of 'quarantined'/'disabled' back to 'active'. It records the
// auditor and, for a quarantined agent, requires the consecutive-success
// counter to have reached the threshold — matching "recovery needs N
// consecutive successful probes". A 'disabled' agent can only be promoted by
// a human override that explicitly accepts the threshold is not met (the
// auditor note carries the reason), so disabled is never auto-promoted but a
// human with authority can still re-activate a withdrawn model.
func (s *TaskService) PromoteAgentHealth(ctx context.Context, agentID, auditorID pgtype.UUID, note string) (db.Agent, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("load agent for health promotion: %w", err)
	}
	state := normalizeHealthState(agent.HealthState.String)
	if state != HealthStateQuarantined && state != HealthStateDisabled {
		// Already assignable; nothing to promote. Idempotent no-op for
		// active/standby avoids a spurious audit row.
		return agent, nil
	}
	meta := unmarshalHealthMetadata(agent.HealthMetadata)
	threshold := meta.RecoverySuccessThreshold
	if threshold <= 0 {
		threshold = DefaultRecoverySuccessThreshold
	}
	// The recovery gate is the pure decision in recoveryGateSatisfied so the
	// matrix and the service path share one source of truth. The error
	// messages below are the durable notice a human reads when a promotion is
	// refused; they name the missing condition explicitly.
	if !recoveryGateSatisfied(state, meta.ConsecutiveSuccesses, threshold, note) {
		if state == HealthStateQuarantined {
			return db.Agent{}, fmt.Errorf("recovery gate: %d/%d consecutive successes recorded; need %d",
				meta.ConsecutiveSuccesses, threshold, threshold)
		}
		return db.Agent{}, fmt.Errorf("recovery gate: promoting a disabled agent requires an auditor note")
	}
	meta.AuditorID = ""
	if auditorID.Valid {
		meta.AuditorID = util.UUIDToString(auditorID)
	}
	meta.AuditorNote = note
	meta.AuditedAt = time.Now().UTC().Format(time.RFC3339Nano)
	// Reset the counter on promotion so a future failure starts from 0.
	meta.ConsecutiveSuccesses = 0
	return s.Queries.UpdateAgentHealthState(ctx, db.UpdateAgentHealthStateParams{
		ID:             agentID,
		HealthState:    textHealthState(HealthStateActive),
		HealthMetadata: marshalHealthMetadata(meta),
	})
}

// SetAgentHealthState is the manual override path: an operator sets an
// agent's health_state and records the auditor + reason. Used by the
// quarantine/disable surface and to place gpt-5.3-codex into 'quarantined'
// during the migration. A human override skips the recovery-threshold gate —
// the auditor's explicit action is the authority — but every override is
// recorded in health_metadata for audit.
func (s *TaskService) SetAgentHealthState(ctx context.Context, agentID, auditorID pgtype.UUID, state AgentHealthState, note string) (db.Agent, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("load agent for health override: %w", err)
	}
	meta := unmarshalHealthMetadata(agent.HealthMetadata)
	meta.AuditorID = ""
	if auditorID.Valid {
		meta.AuditorID = util.UUIDToString(auditorID)
	}
	meta.AuditorNote = note
	meta.AuditedAt = time.Now().UTC().Format(time.RFC3339Nano)
	// Demoting resets the recovery counter; promoting (via override) records
	// the auditor and leaves the counter at 0.
	meta.ConsecutiveSuccesses = 0
	slog.Info("agent health override",
		"agent_id", util.UUIDToString(agentID),
		"from", agent.HealthState.String,
		"to", string(state),
		"auditor", meta.AuditorID,
	)
	return s.Queries.UpdateAgentHealthState(ctx, db.UpdateAgentHealthStateParams{
		ID:             agentID,
		HealthState:    textHealthState(state),
		HealthMetadata: marshalHealthMetadata(meta),
	})
}
