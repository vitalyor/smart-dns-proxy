package api

import (
	"net/http"

	"smartdns/shared/metrics"
)

// Routes returns the public API + UI handler (no agent endpoints).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()

	// --- auth (public) ---
	api.HandleFunc("POST /auth/login", s.wrap("auth.login", s.handleLogin))
	api.HandleFunc("POST /auth/logout", s.wrap("auth.logout", s.handleLogout))
	api.HandleFunc("GET /auth/me", s.wrap("auth.me", s.handleMe))

	// --- auth (session required) ---
	v := s.requireAuth
	api.HandleFunc("GET /auth/sessions", s.wrap("auth.sessions", v("viewer", s.handleSessions)))
	api.HandleFunc("DELETE /auth/sessions/{id}", s.wrap("auth.session.revoke", v("viewer", s.handleRevokeSession)))
	api.HandleFunc("POST /auth/password", s.wrap("auth.password", v("viewer", s.handleChangePassword)))
	api.HandleFunc("POST /auth/totp/setup", s.wrap("auth.totp.setup", v("viewer", s.handleTOTPSetup)))
	api.HandleFunc("POST /auth/totp/enable", s.wrap("auth.totp.enable", v("viewer", s.handleTOTPEnable)))
	api.HandleFunc("POST /auth/totp/disable", s.wrap("auth.totp.disable", v("viewer", s.handleTOTPDisable)))

	// --- dashboard & observability ---
	api.HandleFunc("GET /dashboard", s.wrap("dashboard", v("viewer", s.dashboard)))
	api.HandleFunc("GET /health/summary", s.wrap("health.summary", v("viewer", s.healthSummary)))
	api.HandleFunc("GET /health/samples", s.wrap("health.samples", v("viewer", s.healthSamples)))
	api.HandleFunc("GET /events", s.wrap("events", v("viewer", s.listEvents)))
	api.HandleFunc("GET /audit", s.wrap("audit", v("viewer", s.listAudit)))

	// --- nodes ---
	api.HandleFunc("GET /nodes", s.wrap("nodes.list", v("viewer", s.listNodes)))
	api.HandleFunc("GET /nodes/{id}", s.wrap("nodes.get", v("viewer", s.getNode)))
	api.HandleFunc("PATCH /nodes/{id}", s.wrap("nodes.patch", v("operator", s.patchNode)))
	api.HandleFunc("DELETE /nodes/{id}", s.wrap("nodes.delete", v("owner", s.deleteNode)))
	api.HandleFunc("POST /nodes/{id}/maintenance", s.wrap("nodes.maintenance", v("operator", s.nodeMaintenance)))
	api.HandleFunc("POST /nodes/{id}/certificate", s.wrap("nodes.certificate", v("operator", s.nodeCertificate)))
	api.HandleFunc("GET /nodes/{id}/dns-log", s.wrap("nodes.dnslog", v("viewer", s.nodeDNSLog)))
	api.HandleFunc("POST /nodes", s.wrap("nodes.create", v("operator", s.createNode)))

	// --- groups ---
	for _, k := range []groupKind{ingressKind, egressKind} {
		k := k
		base := "/" + k.role + "-groups"
		api.HandleFunc("GET "+base, s.wrap(k.role+"groups.list", v("viewer", s.listGroups(k))))
		api.HandleFunc("POST "+base, s.wrap(k.role+"groups.create", v("operator", s.createGroup(k))))
		api.HandleFunc("PATCH "+base+"/{id}", s.wrap(k.role+"groups.patch", v("operator", s.patchGroup(k))))
		api.HandleFunc("DELETE "+base+"/{id}", s.wrap(k.role+"groups.delete", v("operator", s.deleteGroup(k))))
		api.HandleFunc("POST "+base+"/{id}/members", s.wrap(k.role+"groups.member.add", v("operator", s.addMember(k))))
		api.HandleFunc("DELETE "+base+"/{id}/members/{node_id}", s.wrap(k.role+"groups.member.remove", v("operator", s.removeMember(k))))
	}

	// --- services ---
	api.HandleFunc("GET /services", s.wrap("services.list", v("viewer", s.listServices)))
	api.HandleFunc("GET /services/catalog", s.wrap("services.catalog", v("viewer", s.serviceCatalog)))
	api.HandleFunc("POST /services", s.wrap("services.create", v("operator", s.createService)))
	api.HandleFunc("POST /services/wizard", s.wrap("services.wizard", v("operator", s.serviceWizard)))
	api.HandleFunc("PATCH /services/{id}", s.wrap("services.patch", v("operator", s.patchService)))
	api.HandleFunc("PATCH /services/{id}/domains", s.wrap("services.domains", v("operator", s.setServiceDomains)))
	api.HandleFunc("GET /services/{id}/sources", s.wrap("services.sources.list", v("viewer", s.listServiceSources)))
	api.HandleFunc("POST /services/{id}/sources", s.wrap("services.source.add", v("operator", s.addServiceSource)))
	api.HandleFunc("DELETE /services/{id}/sources/{source_id}", s.wrap("services.source.delete", v("operator", s.deleteServiceSource)))
	api.HandleFunc("POST /services/{id}/refresh", s.wrap("services.refresh", v("operator", s.refreshService)))
	api.HandleFunc("DELETE /services/{id}", s.wrap("services.delete", v("operator", s.deleteService)))

	// --- rule sets ---
	api.HandleFunc("GET /rule-sets", s.wrap("rulesets.list", v("viewer", s.listRuleSets)))
	api.HandleFunc("POST /rule-sets", s.wrap("rulesets.create", v("operator", s.createRuleSet)))
	api.HandleFunc("GET /rule-sets/{id}", s.wrap("rulesets.get", v("viewer", s.getRuleSet)))
	api.HandleFunc("PATCH /rule-sets/{id}", s.wrap("rulesets.patch", v("operator", s.patchRuleSet)))
	api.HandleFunc("DELETE /rule-sets/{id}", s.wrap("rulesets.delete", v("operator", s.deleteRuleSet)))
	api.HandleFunc("POST /rule-sets/{id}/sources", s.wrap("rulesets.source.add", v("operator", s.addSource)))
	api.HandleFunc("DELETE /rule-sets/{id}/sources/{source_id}", s.wrap("rulesets.source.delete", v("operator", s.deleteSource)))
	api.HandleFunc("POST /rule-sets/{id}/fetch", s.wrap("rulesets.fetch", v("operator", s.fetchRuleSet)))
	api.HandleFunc("GET /rule-sets/{id}/diff", s.wrap("rulesets.diff", v("viewer", s.diffRuleSet)))
	api.HandleFunc("POST /rule-sets/{id}/approve", s.wrap("rulesets.approve", v("operator", s.approveRuleSet)))

	// --- revisions ---
	api.HandleFunc("GET /revisions", s.wrap("revisions.list", v("viewer", s.listRevisions)))
	api.HandleFunc("POST /revisions/compile", s.wrap("revisions.compile", v("operator", s.compileRevision)))
	api.HandleFunc("GET /revisions/{id}", s.wrap("revisions.get", v("viewer", s.getRevision)))
	api.HandleFunc("GET /revisions/{id}/artifacts/{node_id}", s.wrap("revisions.artifact", v("viewer", s.getRevisionArtifact)))
	api.HandleFunc("POST /revisions/{id}/deploy", s.wrap("revisions.deploy", v("operator", s.deployRevision)))
	api.HandleFunc("POST /revisions/{id}/rollback", s.wrap("revisions.rollback", v("operator", s.rollbackRevision)))

	// --- devices & settings ---
	api.HandleFunc("GET /device-profiles", s.wrap("devices.list", v("viewer", s.listDeviceProfiles)))
	api.HandleFunc("POST /device-profiles", s.wrap("devices.create", v("operator", s.createDeviceProfile)))
	api.HandleFunc("DELETE /device-profiles/{id}", s.wrap("devices.delete", v("operator", s.deleteDeviceProfile)))
	api.HandleFunc("GET /device-profiles/{id}/download", s.wrap("devices.download", v("viewer", s.downloadDeviceProfile)))
	api.HandleFunc("GET /settings", s.wrap("settings.get", v("viewer", s.getSettings)))
	api.HandleFunc("PUT /settings", s.wrap("settings.put", v("owner", s.putSettings)))

	// --- backup / restore (moves the whole panel to a new host) ---
	api.HandleFunc("POST /backup", s.wrap("backup.create", v("owner", s.handleBackup)))
	api.HandleFunc("POST /restore", s.wrap("backup.restore", v("owner", s.handleRestore)))

	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", s.authenticate(api)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Cfg.Version})
	})
	mux.Handle("/metrics", metrics.Handler())
	if s.web != nil {
		mux.Handle("/", s.web)
	}
	return accessLog(securityHeaders(s.Cfg.SecureCookies, mux))
}
