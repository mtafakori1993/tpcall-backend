package history

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store against PostgreSQL using parameterized SQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore returns a Store backed by the given connection pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// CreateCall inserts a ringing call. The UNIQUE(call_id) constraint plus the
// ON CONFLICT DO NOTHING guard make duplicate creation a safe no-op, so racing
// signaling events can never produce two rows for one physical call.
func (s *PostgresStore) CreateCall(ctx context.Context, callID uuid.UUID, callerID, calleeID uuid.UUID, startedAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO call_history (id, call_id, caller_user_id, callee_user_id, started_at, end_reason)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (call_id) DO NOTHING`,
		uuid.New(), callID, callerID, calleeID, startedAt, EndReasonNoAnswer,
	)
	return err
}

// MarkAnswered records acceptance only when the call has not been answered yet.
func (s *PostgresStore) MarkAnswered(ctx context.Context, callID uuid.UUID, answeredAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE call_history SET answered_at = $2
		 WHERE call_id = $1 AND answered_at IS NULL`,
		callID, answeredAt,
	)
	return err
}

// MarkEnded is idempotent: the first end event sets ended_at and end_reason,
// and any racing/repeated end processing is a no-op. end_reason cannot be
// guarded with COALESCE: the column is NOT NULL and defaults to no_answer when
// the ringing row is created, so "first end event wins" is keyed on ended_at.
func (s *PostgresStore) MarkEnded(ctx context.Context, callID uuid.UUID, endedAt time.Time, endReason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE call_history
		 SET ended_at = $2, end_reason = $3
		 WHERE call_id = $1 AND ended_at IS NULL`,
		callID, endedAt, endReason,
	)
	return err
}

// ListForUser returns the user's calls (caller or callee), newest first, with
// peer names/extensions resolved via a single JOIN so the API never needs N+1
// lookups. hasMore reports whether another page exists.
func (s *PostgresStore) ListForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Record, bool, error) {
	// Fetch one extra row to detect whether a next page exists.
	rows, err := s.pool.Query(ctx,
		`SELECT ch.id, ch.call_id, ch.caller_user_id, ch.callee_user_id,
		        ch.started_at, ch.answered_at, ch.ended_at, ch.end_reason,
		        caller.name, caller.extension,
		        callee.name, callee.extension
		 FROM call_history ch
		 JOIN users caller ON caller.id = ch.caller_user_id
		 JOIN users callee ON callee.id = ch.callee_user_id
		 WHERE ch.caller_user_id = $1 OR ch.callee_user_id = $1
		 ORDER BY ch.started_at DESC, ch.call_id DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit+1, offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(
			&r.ID, &r.CallID, &r.CallerUserID, &r.CalleeUserID,
			&r.StartedAt, &r.AnsweredAt, &r.EndedAt, &r.EndReason,
			&r.CallerName, &r.CallerExtension,
			&r.CalleeName, &r.CalleeExtension,
		); err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// ListAll returns all calls newest first (admin view) with a single JOIN.
func (s *PostgresStore) ListAll(ctx context.Context, limit, offset int) ([]Record, bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT ch.id, ch.call_id, ch.caller_user_id, ch.callee_user_id,
		        ch.started_at, ch.answered_at, ch.ended_at, ch.end_reason,
		        caller.name, caller.extension,
		        callee.name, callee.extension
		 FROM call_history ch
		 JOIN users caller ON caller.id = ch.caller_user_id
		 JOIN users callee ON callee.id = ch.callee_user_id
		 ORDER BY ch.started_at DESC, ch.call_id DESC
		 LIMIT $1 OFFSET $2`,
		limit+1, offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(
			&r.ID, &r.CallID, &r.CallerUserID, &r.CalleeUserID,
			&r.StartedAt, &r.AnsweredAt, &r.EndedAt, &r.EndReason,
			&r.CallerName, &r.CallerExtension,
			&r.CalleeName, &r.CalleeExtension,
		); err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// Stats aggregates counters in two small queries.
func (s *PostgresStore) Stats(ctx context.Context, since time.Time) (Stats, error) {
	st := Stats{ByReason: map[string]int64{}}
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*),
		        COUNT(*) FILTER (WHERE started_at >= $1),
		        COUNT(answered_at),
		        COALESCE(SUM(EXTRACT(EPOCH FROM (ended_at - answered_at)))
		                 FILTER (WHERE answered_at IS NOT NULL AND ended_at IS NOT NULL), 0)::bigint
		 FROM call_history`, since,
	).Scan(&st.CallsTotal, &st.CallsSince, &st.Answered, &st.TalkSecondsTotal)
	if err != nil {
		return Stats{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT end_reason, COUNT(*) FROM call_history GROUP BY end_reason`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var n int64
		if err := rows.Scan(&reason, &n); err != nil {
			return Stats{}, err
		}
		st.ByReason[reason] = n
	}
	return st, rows.Err()
}
