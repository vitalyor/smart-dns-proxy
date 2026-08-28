package api

import (
	"fmt"
	"net/http"
	"strings"

	"smartdns/panel/internal/store"
)

type groupKind struct {
	table       string
	memberTable string
	role        string
	modes       []string
	label       string
}

var ingressKind = groupKind{"ingress_groups", "ingress_group_members", "ingress",
	[]string{"active_active", "primary_fallback", "weighted"}, "ingress-группа"}
var egressKind = groupKind{"egress_groups", "egress_group_members", "egress",
	[]string{"primary_fallback", "weighted", "lowest_latency", "manual_fixed"}, "egress-группа"}

func (s *Server) listGroups(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		groups, err := store.Many[store.Group](r.Context(), s.DB,
			fmt.Sprintf(`SELECT * FROM %s ORDER BY name`, k.table))
		if err != nil {
			return err
		}
		members, err := store.Many[store.GroupMember](r.Context(), s.DB, fmt.Sprintf(`
			SELECT m.id, m.group_id, m.node_id, m.priority, m.weight, m.enabled,
			       n.name AS node_name, n.role, n.status
			FROM %s m JOIN nodes n ON n.id = m.node_id
			ORDER BY m.priority, n.name`, k.memberTable))
		if err != nil {
			return err
		}
		byGroup := map[string][]store.GroupMember{}
		for _, m := range members {
			byGroup[m.GroupID] = append(byGroup[m.GroupID], m)
		}
		out := make([]map[string]any, 0, len(groups))
		for _, g := range groups {
			ms := byGroup[g.ID]
			if ms == nil {
				ms = []store.GroupMember{}
			}
			out = append(out, map[string]any{"group": g, "members": ms})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
		return nil
	}
}

type groupRequest struct {
	Name     string         `json:"name"`
	Mode     string         `json:"mode"`
	Settings map[string]any `json:"settings"`
}

func (s *Server) createGroup(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req groupRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			return badRequest("укажите название группы")
		}
		if req.Mode == "" {
			req.Mode = k.modes[0]
		}
		if !contains(k.modes, req.Mode) {
			return badRequest("режим %q недопустим для %s (доступно: %s)", req.Mode, k.label, strings.Join(k.modes, ", "))
		}
		if req.Settings == nil {
			req.Settings = map[string]any{}
		}
		g, err := store.One[store.Group](r.Context(), s.DB,
			fmt.Sprintf(`INSERT INTO %s (name, mode, settings) VALUES ($1,$2,$3) RETURNING *`, k.table),
			req.Name, req.Mode, req.Settings)
		if err != nil {
			return err
		}
		s.audit(r.Context(), r, k.role+"_group.created", k.role+"_group", g.ID, nil, g)
		writeJSON(w, http.StatusCreated, g)
		return nil
	}
}

func (s *Server) patchGroup(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Name     *string        `json:"name"`
			Mode     *string        `json:"mode"`
			Settings map[string]any `json:"settings"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Mode != nil && !contains(k.modes, *req.Mode) {
			return badRequest("режим %q недопустим для %s", *req.Mode, k.label)
		}
		ver, err := ifMatch(r)
		if err != nil {
			return err
		}
		id := r.PathValue("id")
		before, err := store.One[store.Group](r.Context(), s.DB, fmt.Sprintf(`SELECT * FROM %s WHERE id=$1`, k.table), id)
		if err != nil {
			return err
		}
		n, err := s.DB.ExecN(r.Context(), fmt.Sprintf(`
			UPDATE %s SET name=COALESCE($3,name), mode=COALESCE($4,mode),
				settings=COALESCE($5,settings), updated_at=now(), version=version+1
			WHERE id=$1 AND ($2 = 0 OR version = $2)`, k.table),
			id, ver, req.Name, req.Mode, req.Settings)
		if err != nil {
			return err
		}
		if err := checkVersion(n, ver); err != nil {
			return err
		}
		after, _ := store.One[store.Group](r.Context(), s.DB, fmt.Sprintf(`SELECT * FROM %s WHERE id=$1`, k.table), id)
		s.audit(r.Context(), r, k.role+"_group.updated", k.role+"_group", id, before, after)
		writeJSON(w, http.StatusOK, after)
		return nil
	}
}

func (s *Server) deleteGroup(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		id := r.PathValue("id")
		col := "ingress_group_id"
		if k.role == "egress" {
			col = "egress_group_id"
		}
		users, err := store.Many[struct {
			Kind string `db:"kind" json:"kind"`
			Name string `db:"name" json:"name"`
			ID   string `db:"id" json:"id"`
		}](r.Context(), s.DB, fmt.Sprintf(`SELECT 'service' AS kind, name, id::text FROM services WHERE %s = $1`, col), id)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			e := conflictErr("группа используется сервисами; сначала переназначьте их")
			e.Details = users
			return e
		}
		n, err := s.DB.ExecN(r.Context(), fmt.Sprintf(`DELETE FROM %s WHERE id=$1`, k.table), id)
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("group")
		}
		s.audit(r.Context(), r, k.role+"_group.deleted", k.role+"_group", id, nil, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return nil
	}
}

type memberRequest struct {
	NodeID   string `json:"node_id"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Enabled  *bool  `json:"enabled"`
}

func (s *Server) addMember(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req memberRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		gid := r.PathValue("id")
		role, err := store.Value[string](r.Context(), s.DB, `SELECT role FROM nodes WHERE id=$1`, req.NodeID)
		if err != nil {
			return notFound("node")
		}
		if role != k.role {
			return badRequest("нода имеет роль %q и не может входить в %s", role, k.label)
		}
		if req.Priority <= 0 {
			req.Priority = 1
		}
		if req.Weight <= 0 {
			req.Weight = 1
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		_, err = s.DB.Exec(r.Context(), fmt.Sprintf(`
			INSERT INTO %s (group_id, node_id, priority, weight, enabled) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (group_id, node_id) DO UPDATE
			SET priority=EXCLUDED.priority, weight=EXCLUDED.weight, enabled=EXCLUDED.enabled`, k.memberTable),
			gid, req.NodeID, req.Priority, req.Weight, enabled)
		if err != nil {
			return err
		}
		s.audit(r.Context(), r, k.role+"_group.member_added", k.role+"_group", gid, nil, req)
		writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
		return nil
	}
}

func (s *Server) removeMember(k groupKind) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		gid, nid := r.PathValue("id"), r.PathValue("node_id")
		n, err := s.DB.ExecN(r.Context(),
			fmt.Sprintf(`DELETE FROM %s WHERE group_id=$1 AND node_id=$2`, k.memberTable), gid, nid)
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("member")
		}
		s.audit(r.Context(), r, k.role+"_group.member_removed", k.role+"_group", gid, map[string]any{"node_id": nid}, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
		return nil
	}
}
