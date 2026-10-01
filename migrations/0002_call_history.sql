-- Call history: one row per physical call, persisted authoritatively by the
-- backend call state machine.

CREATE TABLE call_history (
    id              UUID PRIMARY KEY,
    call_id         UUID NOT NULL UNIQUE,

    caller_user_id  UUID NOT NULL REFERENCES users(id),
    callee_user_id  UUID NOT NULL REFERENCES users(id),

    started_at      TIMESTAMPTZ NOT NULL,
    answered_at     TIMESTAMPTZ,
    ended_at        TIMESTAMPTZ,

    end_reason      VARCHAR(32) NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Retrieval is always scoped to a single user (caller OR callee) and sorted
-- newest-first, so both sides of the lookup benefit from a composite index.
CREATE INDEX idx_call_history_caller_started ON call_history (caller_user_id, started_at DESC);
CREATE INDEX idx_call_history_callee_started ON call_history (callee_user_id, started_at DESC);
