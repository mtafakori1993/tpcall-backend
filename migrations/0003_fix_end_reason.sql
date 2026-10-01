-- Repair call_history.end_reason. MarkEnded used COALESCE(end_reason, ...) on a NOT NULL column that
-- defaults to 'no_answer', so every call kept 'no_answer' regardless of how it really ended.
-- Calls that were answered and then ended are certainly 'completed'. For unanswered calls the real
-- reason (rejected / cancelled / no_answer / disconnect) is unrecoverable and is left as is.
UPDATE call_history
SET end_reason = 'completed'
WHERE answered_at IS NOT NULL
  AND ended_at IS NOT NULL
  AND end_reason = 'no_answer';
