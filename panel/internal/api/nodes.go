package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/store"
	"smartdns/shared/model"
	"smartdns/shared/pki"
)

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		store.Node
		DesiredSeq *int64   `db:"desired_sequence" json:"desired_sequence"`
		Groups     []string `db:"groups" json:"groups"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT n.*, dr.sequence AS desired_sequence,
		       COALESCE(
		         (SELECT array_agg(g.name ORDER BY g.name) FROM ingress_group_members m
		            JOIN ingress_groups g ON g.id = m.group_id WHERE m.node_id = n.id)
		         || COALESCE((SELECT array_agg(g.name ORDER BY g.name) FROM egress_group_members m
		            JOIN egress_groups g ON g.id = m.group_id WHERE m.node_id = n.id), '{}'),
		         '{}') AS groups
		FROM nodes n
		LEFT JOIN revisions dr ON dr.id = n.desired_revision_id
		LEFT JOIN revisions ar ON ar.id = n.applied_revision_id
		ORDER BY n.role, n.name`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) error {
	n, err := store.One[store.Node](r.Context(), s.DB, `SELECT * FROM nodes WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	deployments, err := store.Many[store.NodeDeployment](r.Context(), s.DB, `
		SELECT d.*, n.name AS node_name FROM node_deployments d JOIN nodes n ON n.id = d.node_id
		WHERE d.node_id=$1 ORDER BY d.started_at DESC LIMIT 20`, n.ID)
	if err != nil {
		return err
	}
	samples, err := store.Many[store.HealthSample](r.Context(), s.DB,
		`SELECT * FROM health_samples WHERE node_id=$1 ORDER BY observed_at DESC LIMIT 50`, n.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": n, "deployments": deployments, "health": samples})
	return nil
}

type nodePatch struct {
	Name          *string `json:"name"`
	PublicIPv4    *string `json:"public_ipv4"`
	PublicIPv6    *string `json:"public_ipv6"`
	RelayEndpoint *string `json:"relay_endpoint"`
	RelaySNI      *string `json:"relay_sni"`
	MgmtAddress   *string `json:"mgmt_address"`
	Region        *string `json:"region"`
	Country       *string `json:"country"`
	Notes         *string `json:"notes"`
	Status        *string `json:"status"`
}

func (s *Server) patchNode(w http.ResponseWriter, r *http.Request) error {
	var p nodePatch
	if err := decodeJSON(r, &p); err != nil {
		return err
	}
	ver, err := ifMatch(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	before, err := store.One[store.Node](r.Context(), s.DB, `SELECT * FROM nodes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if p.Status != nil && !contains([]string{"healthy", "degraded", "unhealthy", "maintenance", "disabled", "unknown"}, *p.Status) {
		return badRequest("недопустимый статус %q", *p.Status)
	}
	n, err := s.DB.ExecN(r.Context(), `
		UPDATE nodes SET
			name = COALESCE($3, name),
			public_ipv4 = COALESCE($4, public_ipv4),
			public_ipv6 = COALESCE($5, public_ipv6),
			relay_endpoint = COALESCE($6, relay_endpoint),
			relay_sni = COALESCE($7, relay_sni),
			mgmt_address = COALESCE($8, mgmt_address),
			region = COALESCE($9, region),
			country = COALESCE($10, country),
			notes = COALESCE($11, notes),
			status = COALESCE($12, status),
			updated_at = now(), version = version + 1
		WHERE id = $1 AND ($2 = 0 OR version = $2)`,
		id, ver, p.Name, p.PublicIPv4, p.PublicIPv6, p.RelayEndpoint, p.RelaySNI,
		p.MgmtAddress, p.Region, p.Country, p.Notes, p.Status)
	if err != nil {
		return err
	}
	if err := checkVersion(n, ver); err != nil {
		return err
	}
	after, _ := store.One[store.Node](r.Context(), s.DB, `SELECT * FROM nodes WHERE id=$1`, id)
	s.audit(r.Context(), r, "node.updated", "node", id, before, after)
	writeJSON(w, http.StatusOK, after)
	return nil
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	deps, err := store.Many[struct {
		Kind string `db:"kind" json:"kind"`
		Name string `db:"name" json:"name"`
	}](r.Context(), s.DB, `
		SELECT 'ingress_group' AS kind, g.name FROM ingress_group_members m JOIN ingress_groups g ON g.id=m.group_id WHERE m.node_id=$1
		UNION ALL
		SELECT 'egress_group', g.name FROM egress_group_members m JOIN egress_groups g ON g.id=m.group_id WHERE m.node_id=$1`, id)
	if err != nil {
		return err
	}
	if len(deps) > 0 {
		e := conflictErr("нода используется в группах; сначала удалите её из них")
		e.Details = deps
		return e
	}
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM nodes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("node")
	}
	s.audit(r.Context(), r, "node.deleted", "node", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

func (s *Server) nodeMaintenance(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	status := "unknown"
	if req.Enabled {
		status = "maintenance"
	}
	id := r.PathValue("id")
	n, err := s.DB.ExecN(r.Context(),
		`UPDATE nodes SET status=$2, updated_at=now(), version=version+1 WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("node")
	}
	s.audit(r.Context(), r, "node.maintenance", "node", id, nil, map[string]any{"enabled": req.Enabled})
	s.event(r.Context(), "info", "panel", "node_maintenance",
		fmt.Sprintf("Нода переведена в режим обслуживания: %v", req.Enabled), &id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
	return nil
}

// --- node creation & bundle -------------------------------------------------

type createNodeRequest struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	MgmtAddress string `json:"mgmt_address"`
	PublicIPv4  string `json:"public_ipv4"`
	PublicIPv6  string `json:"public_ipv6"`
	RelayPort   int    `json:"relay_port"`
}

// createNode registers a node and mints its provisioning bundle. Under the push
// model the panel connects out to the node, so there is no one-time token: the
// bundle carries the node's TLS server identity and pins this panel, exactly
// like remnanode's SECRET_KEY. The bundle is shown once and never stored.
func (s *Server) createNode(w http.ResponseWriter, r *http.Request) error {
	var req createNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Role != "ingress" && req.Role != "egress" {
		return badRequest("role должен быть ingress или egress")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Role + "-" + auth.RandomToken(4)
	}
	mgmt := strings.TrimSpace(req.MgmtAddress)
	if mgmt == "" {
		host := req.PublicIPv4
		if host == "" {
			host = req.PublicIPv6
		}
		if host != "" {
			mgmt = host + ":3333"
		}
	}

	return s.idempotent(w, r, "create_node", func() (int, any, error) {
		relayEndpoint := ""
		if req.Role == "egress" {
			port := req.RelayPort
			if port == 0 {
				port = 8443
			}
			host := req.PublicIPv4
			if host == "" {
				host = req.PublicIPv6
			}
			if host != "" {
				relayEndpoint = fmt.Sprintf("%s:%d", host, port)
			}
		}
		ctx := r.Context()
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return 0, nil, err
		}
		defer tx.Rollback(ctx)

		var nodeID string
		err = tx.QueryRow(ctx, `
			INSERT INTO nodes (name, role, mgmt_address, public_ipv4, public_ipv6, relay_endpoint, relay_sni, status)
			VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$1,'unknown')
			RETURNING id`, name, req.Role, mgmt, req.PublicIPv4, req.PublicIPv6, relayEndpoint).Scan(&nodeID)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: имя ноды %q уже занято", store.ErrConflict, name)
		}

		caCert, caKey, err := pki.LoadCA(s.Cfg.CACertPEM, s.Cfg.CAKeyPEM)
		if err != nil {
			return 0, nil, internal(err)
		}
		// The node's TLS *server* identity. SANs cover the name plus any public
		// address the panel might dial by; a bare-IP dial is pinned separately.
		certPEM, keyPEM, err := pki.Issue(caCert, caKey, pki.CSRRequest{
			CommonName: name, Role: req.Role,
			DNSNames: []string{name, "localhost"},
			IPs:      nonEmptyIPs(req.PublicIPv4, req.PublicIPv6, hostOf(mgmt)),
			TTL:      397 * 24 * time.Hour,
		})
		if err != nil {
			return 0, nil, internal(err)
		}
		fp, _ := pki.Fingerprint(certPEM)
		serial, nb, na, _ := pki.SerialOf(certPEM)
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_identities (node_id, cert_serial, cert_pem, fingerprint, not_before, not_after)
			VALUES ($1,$2,$3,$4,$5,$6)`, nodeID, serial, string(certPEM), fp, nb, na); err != nil {
			return 0, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, nil, err
		}

		bundle := model.Bundle{
			NodeID: nodeID, Name: name, Role: req.Role,
			NodeCertPEM:   string(certPEM),
			NodeKeyPEM:    string(keyPEM),
			CACertPEM:     string(s.Cfg.CACertPEM),
			PanelClientFP: s.Cfg.PanelClientFP,
		}
		s.audit(ctx, r, "node.created", "node", nodeID, nil,
			map[string]any{"role": req.Role, "name": name, "mgmt_address": mgmt})
		s.event(ctx, "info", "panel", "node_created", "Создана нода "+name, &nodeID, nil)

		install := fmt.Sprintf("sudo bash install-node.sh --role %s --bundle %s", req.Role, bundle.Encode())
		return http.StatusCreated, map[string]any{
			"node_id":          nodeID,
			"name":             name,
			"role":             req.Role,
			"mgmt_address":     mgmt,
			"bundle":           bundle.Encode(), // shown once
			"cert_fingerprint": fp,
			"install_command":  install,
		}, nil
	})
}

func nonEmptyIPs(vals ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range vals {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func hostOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
