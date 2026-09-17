package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// textState is a constructor shorthand for a valid pgtype.Text health_state
// value, so the matrix below reads as the state it is exercising.
func textState(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// TestAgentHealthGateRoutingMatrix is the regression for RIC-806 acceptance
// criterion 1 + 2: a machine-readable health_state of quarantined or disabled
// must block routing, and the gate is the column value — NOT the description
// text. The pre-RIC-806 "QUARANTINED" description marker never reached the
// router; this matrix proves the gate reads health_state and refuses.
//
// The matrix deliberately exercises every state so a future value set change
// cannot silently admit a gated agent: active and standby defer to the
// runtime verdict (the gate returns available), while quarantined and
// disabled return a blocked verdict with the dedicated reason code that
// distinguishes a health decision from an archived row or a machine repair.
func TestAgentHealthGateRoutingMatrix(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		valid    bool // pgtype.Text validity
		blocked  bool
		reason   dispatch.ReasonCode
	 assignable bool
	}{
		// NULL/empty — a pre-migration or never-probed agent — is active by
		// construction so the migration does not gate out the 25 existing
		// agents the issue names.
		{name: "NULL column", state: "", valid: false, blocked: false, assignable: true},
		{name: "empty string", state: "", valid: true, blocked: false, assignable: true},
		{name: "active", state: "active", valid: true, blocked: false, assignable: true},
		// Standby is visible but held out of AUTOMATIC assignment; the gate
		// itself does not block it (a human may still dispatch), so the
		// reason code is not set here.
		{name: "standby", state: "standby", valid: true, blocked: false, assignable: false},
		{name: "quarantined", state: "quarantined", valid: true, blocked: true, reason: dispatch.ReasonAgentQuarantined, assignable: false},
		{name: "disabled", state: "disabled", valid: true, blocked: true, reason: dispatch.ReasonAgentQuarantined, assignable: false},
		// An out-of-CHECK-set value is impossible for rows written after
		// migration 400, but the gate must refuse rather than admit it.
		{name: "unknown value quarantines", state: "frob", valid: true, blocked: true, reason: dispatch.ReasonAgentQuarantined, assignable: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := db.Agent{HealthState: pgtype.Text{String: tc.state, Valid: tc.valid}}
			got := AgentHealthGate(agent)
			if tc.blocked {
				if !got.Blocked() {
					t.Fatalf("state %q: got %+v, want blocked", tc.state, got)
				}
				if got.Reason != tc.reason {
					t.Errorf("state %q: reason = %q, want %q", tc.state, got.Reason, tc.reason)
				}
			} else {
				if got.Blocked() {
					t.Fatalf("state %q: got %+v, want not blocked (defer to runtime)", tc.state, got)
				}
				if got.Reason != dispatch.ReasonCode("") {
					t.Errorf("state %q: non-blocked verdict must not carry a reason, got %q", tc.state, got.Reason)
				}
			}
			// The assignable half mirrors the gate for quarantined/disabled
			// and proves standby is held out of automatic assignment even
			// though the gate does not hard-block it.
			state := normalizeHealthState(agent.HealthState.String)
			if got := state.IsAssignable(); got != tc.assignable {
				t.Errorf("state %q: IsAssignable = %v, want %v", tc.state, got, tc.assignable)
			}
			if got := state.IsBlocked(); got != tc.blocked {
				t.Errorf("state %q: IsBlocked = %v, want %v", tc.state, got, tc.blocked)
			}
		})
	}
}

// TestAgentHealthGateAuditorNoteInDetail proves the blocked verdict surfaces
// the auditor's note so the durable notice a user reads on a refused trigger
// attributes the decision (RIC-806 acceptance criterion 4: "异常自动降级，
// 不自动改业务Issue状态" + "恢复需连续N次探测成功并记录审核人"). The detail
// is the only channel the reason text reaches the user; it must carry the
// auditor note when one was recorded.
func TestAgentHealthGateAuditorNoteInDetail(t *testing.T) {
	agent := db.Agent{
		HealthState: textState("quarantined"),
		// Probe registry carrying an auditor note from a manual override.
		HealthMetadata: []byte(`{"auditor_id":"0dcf0b8f-8acd-4ae7-9a28-39182ef4e693","auditor_note":"model withdrawn upstream, see RIC-804","audited_at":"2026-09-17T00:00:00Z"}`),
	}
	got := AgentHealthGate(agent)
	if !got.Blocked() || got.Reason != dispatch.ReasonAgentQuarantined {
		t.Fatalf("quarantined agent: got %+v, want blocked/agent_quarantined", got)
	}
	if !strings.Contains(got.Detail, "quarantined") {
		t.Errorf("detail must name the state, got %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "RIC-804") {
		t.Errorf("detail must carry the auditor note, got %q", got.Detail)
	}
	// Malformed metadata must never change the routing decision — the gate
	// falls back to the state alone, matching the runtime_offline contract.
	badMeta := db.Agent{HealthState: textState("quarantined"), HealthMetadata: []byte(`{not json`)}
	bad := AgentHealthGate(badMeta)
	if !bad.Blocked() || bad.Reason != dispatch.ReasonAgentQuarantined {
		t.Fatalf("malformed metadata must not downgrade the block: got %+v", bad)
	}
	if !strings.Contains(bad.Detail, "quarantined") {
		t.Errorf("malformed metadata: detail must still name the state, got %q", bad.Detail)
	}
}

// TestHealthBlockedIsErrAgentHealthBlocked is the regression for RIC-806
// acceptance criterion 2 at the enqueue chokepoint: a quarantined/disabled
// agent returns a sentinel the dispatch path classifies by errors.Is, never
// by string-matching. The sentinel carries the state so the durable notice
// can render the auditor + reason. This is the filter the description-text
// "QUARANTINED" markers never provided.
func TestHealthBlockedIsErrAgentHealthBlocked(t *testing.T) {
	// active: no error.
	if err := healthBlocked(db.Agent{HealthState: textState("active")}); err != nil {
		t.Errorf("active agent: healthBlocked = %v, want nil", err)
	}
	// quarantined: sentinel.
	qErr := healthBlocked(db.Agent{HealthState: textState("quarantined")})
	var target *ErrAgentHealthBlocked
	if !errors.As(qErr, &target) {
		t.Fatalf("quarantined: got %T, want *ErrAgentHealthBlocked", qErr)
	}
	if target.State != HealthStateQuarantined {
		t.Errorf("quarantined sentinel state = %q, want quarantined", target.State)
	}
	// disabled: sentinel, distinct state.
	dErr := healthBlocked(db.Agent{HealthState: textState("disabled")})
	var dTarget *ErrAgentHealthBlocked
	if !errors.As(dErr, &dTarget) {
		t.Fatalf("disabled: got %T, want *ErrAgentHealthBlocked", dErr)
	}
	if dTarget.State != HealthStateDisabled {
		t.Errorf("disabled sentinel state = %q, want disabled", dTarget.State)
	}
	// The message must name the state so logs and a wrapped notice stay
	// actionable; this is what a user reads when a trigger is refused.
	if !strings.Contains(qErr.Error(), "quarantined") {
		t.Errorf("sentinel message must name the state, got %q", qErr.Error())
	}
}

// TestHealthMetadataRoundTrip covers the probe-registry serialization
// contract (RIC-806 acceptance criterion 3): last_probe/status/error/latency
// and the recovery counter round-trip through the JSONB column without
// distortion. A nil column yields an empty registry that never changes a
// routing decision.
func TestHealthMetadataRoundTrip(t *testing.T) {
	in := HealthMetadata{
		LastProbeAt:              "2026-09-17T00:00:00Z",
		LastStatus:               "error",
		LastError:                "context deadline exceeded",
		LastLatencyMs:            5400,
		ConsecutiveSuccesses:     0,
		RecoverySuccessThreshold: 3,
		AuditorID:                "auto",
		AuditorNote:              "automated degradation: probe failed",
		AuditedAt:                "2026-09-17T00:00:00Z",
	}
	out := unmarshalHealthMetadata(marshalHealthMetadata(in))
	if out.LastStatus != "error" || out.LastError != "context deadline exceeded" {
		t.Errorf("probe result did not round-trip: %+v", out)
	}
	if out.RecoverySuccessThreshold != 3 {
		t.Errorf("threshold did not round-trip: got %d, want 3", out.RecoverySuccessThreshold)
	}
	// Empty/nil column: no probe has run; the registry is empty and the gate
	// must still decide from the state column alone.
	empty := unmarshalHealthMetadata(nil)
	if empty.LastStatus != "" || empty.ConsecutiveSuccesses != 0 {
		t.Errorf("nil column must yield empty registry, got %+v", empty)
	}
	// Malformed JSONB must not corrupt routing — the gate treats it as empty.
	malformed := unmarshalHealthMetadata([]byte(`{"last_status": "ok"`))
	if malformed.LastStatus != "" {
		t.Errorf("malformed metadata must fall back to empty, got %+v", malformed)
	}
}

// TestRecoveryGateThresholdLogic is the regression for RIC-806 acceptance
// criterion 4: "恢复需连续N次探测成功并记录审核人" (recovery requires N
// consecutive successful probes and records an auditor). The pure decision
// lives in recoveryGateSatisfied, shared by PromoteAgentHealth so the matrix
// below and the service path cannot drift. It documents that:
//   - the default threshold is 3 (DefaultRecoverySuccessThreshold);
//   - a per-agent threshold of 0 falls back to the default;
//   - the gate is the consecutive-success counter reaching the threshold
//     WHILE the state is quarantined.
func TestRecoveryGateThresholdLogic(t *testing.T) {
	if DefaultRecoverySuccessThreshold != 3 {
		t.Errorf("default threshold drifted: got %d, want 3", DefaultRecoverySuccessThreshold)
	}
	// A per-agent threshold of 0 must fall back to the default, so a row that
	// never recorded one is not gated by an impossible 0-success bar.
	emptyMeta := HealthMetadata{}
	threshold := emptyMeta.RecoverySuccessThreshold
	if threshold <= 0 {
		threshold = DefaultRecoverySuccessThreshold
	}
	if threshold != DefaultRecoverySuccessThreshold {
		t.Errorf("zero per-agent threshold must fall back to default: got %d", threshold)
	}
	// The recovery counter reaching the threshold while quarantined is the
	// promotion condition; below it, the gate refuses. The matrix below is
	// the pure-logic mirror of PromoteAgentHealth's check.
	cases := []struct {
		name             string
		state            AgentHealthState
		successes        int
		threshold        int
		note             string
		wantPromotable  bool
	}{
		{name: "quarantined below threshold", state: HealthStateQuarantined, successes: 1, threshold: 3, wantPromotable: false},
		{name: "quarantined at threshold", state: HealthStateQuarantined, successes: 3, threshold: 3, wantPromotable: true},
		{name: "quarantined above threshold", state: HealthStateQuarantined, successes: 5, threshold: 3, wantPromotable: true},
		{name: "quarantined default threshold", state: HealthStateQuarantined, successes: 3, threshold: 0, wantPromotable: true},
		// A disabled agent is never auto-promoted; a human override must pass
		// a non-empty note (the auditor's explicit reason). The note gate is
		// the audit record the issue requires.
		{name: "disabled without note refused", state: HealthStateDisabled, successes: 3, threshold: 3, note: "", wantPromotable: false},
		{name: "disabled with note allowed", state: HealthStateDisabled, successes: 3, threshold: 3, note: "re-activate withdrawn model after vendor fix", wantPromotable: true},
		// active/standby: nothing to promote — idempotent.
		{name: "active no-op", state: HealthStateActive, successes: 0, threshold: 3, wantPromotable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// recoveryGateSatisfied is the canonical decision shared with
			// PromoteAgentHealth; the matrix fixes it without a DB so the
			// service path cannot drift.
			got := recoveryGateSatisfied(tc.state, tc.successes, tc.threshold, tc.note)
			if got != tc.wantPromotable {
				t.Errorf("recovery gate: state=%s successes=%d threshold=%d note=%q: got %v, want %v",
					tc.state, tc.successes, tc.threshold, tc.note, got, tc.wantPromotable)
			}
		})
	}
}
