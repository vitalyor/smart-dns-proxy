package api

import (
	"log/slog"

	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
	"smartdns/shared/model"
)

// activeTokenSet returns the DoH path tokens that should be accepted right now:
// profiles the operator made for themselves, plus devices of subscribers who are
// enabled, unexpired and inside their query limit.
//
// Because access is recomputed from this one query, disabling a subscriber,
// letting them expire or blowing a quota needs no separate revocation path —
// their tokens simply stop being in the set, and the next reconcile removes them
// from every node (ADR 0012).
func (s *Server) activeTokenSet(ctx contextT) ([]string, error) {
	type row struct {
		Hash string `db:"token_hash"`
	}
	rows, err := store.Many[row](ctx, s.DB, `
		SELECT d.token_hash
		FROM device_profiles d
		LEFT JOIN subscribers s ON s.id = d.subscriber_id
		WHERE d.token_hash <> ''
		  AND d.revoked_at IS NULL
		  AND ( d.subscriber_id IS NULL
		     OR ( s.enabled
		          AND (s.expires_at IS NULL OR s.expires_at > now())
		          AND (s.query_limit IS NULL OR s.queries_used < s.query_limit) ) )`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Hash)
	}
	return out, nil
}

// reconcileAccess pushes the set when the node's reported digest differs from
// ours. Comparing digests during the ordinary health poll means drift repairs
// itself: a push that never landed is simply retried next tick.
func (s *Server) reconcileAccess(ctx contextT, t pusher.Target, nodeHash string) {
	tokens, err := s.activeTokenSet(ctx)
	if err != nil {
		return
	}
	if model.AccessHash(tokens) == nodeHash {
		return
	}
	if err := s.Cfg.Pusher.PushAccess(ctx, t, model.AccessSet{Tokens: tokens}); err != nil {
		slog.Warn("cannot push access set", "node", t.Name, "err", err)
		return
	}
	slog.Info("access set pushed", "node", t.Name, "tokens", len(tokens))
}
