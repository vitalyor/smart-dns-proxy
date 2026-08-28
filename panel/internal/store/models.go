package store

import (
	"time"
)

// Node is a registered ingress or egress machine.
type Node struct {
	ID                string         `db:"id" json:"id"`
	Name              string         `db:"name" json:"name"`
	Role              string         `db:"role" json:"role"`
	PublicIPv4        *string        `db:"public_ipv4" json:"public_ipv4"`
	PublicIPv6        *string        `db:"public_ipv6" json:"public_ipv6"`
	RelayEndpoint     *string        `db:"relay_endpoint" json:"relay_endpoint"`
	RelaySNI          *string        `db:"relay_sni" json:"relay_sni"`
	Region            string         `db:"region" json:"region"`
	Country           string         `db:"country" json:"country"`
	AgentVersion      string         `db:"agent_version" json:"agent_version"`
	Status            string         `db:"status" json:"status"`
	MgmtAddress       string         `db:"mgmt_address" json:"mgmt_address"`
	DesiredRevisionID *string        `db:"desired_revision_id" json:"desired_revision_id"`
	AppliedRevisionID *string        `db:"applied_revision_id" json:"applied_revision_id"`
	AppliedSequence   int64          `db:"applied_sequence" json:"applied_sequence"`
	Health            map[string]any `db:"health" json:"health"`
	LastSeenAt        *time.Time     `db:"last_seen_at" json:"last_seen_at"`
	LastError         string         `db:"last_error" json:"last_error"`
	Notes             string         `db:"notes" json:"notes"`
	Version           int64          `db:"version" json:"version"`
	CreatedAt         time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at" json:"updated_at"`
}

// Group is an ingress or egress group.
type Group struct {
	ID        string         `db:"id" json:"id"`
	Name      string         `db:"name" json:"name"`
	Mode      string         `db:"mode" json:"mode"`
	Settings  map[string]any `db:"settings" json:"settings"`
	Version   int64          `db:"version" json:"version"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt time.Time      `db:"updated_at" json:"updated_at"`
}

// GroupMember binds a node to a group with priority and weight.
type GroupMember struct {
	ID       string `db:"id" json:"id"`
	GroupID  string `db:"group_id" json:"group_id"`
	NodeID   string `db:"node_id" json:"node_id"`
	NodeName string `db:"node_name" json:"node_name"`
	Role     string `db:"role" json:"role"`
	Status   string `db:"status" json:"status"`
	Priority int    `db:"priority" json:"priority"`
	Weight   int    `db:"weight" json:"weight"`
	Enabled  bool   `db:"enabled" json:"enabled"`
}

// RuleSet groups sources into one normalized domain list.
type RuleSet struct {
	ID              string     `db:"id" json:"id"`
	Name            string     `db:"name" json:"name"`
	Description     string     `db:"description" json:"description"`
	UpdateMode      string     `db:"update_mode" json:"update_mode"`
	IntervalSec     int        `db:"interval_sec" json:"interval_sec"`
	AllowRegex      bool       `db:"allow_regex" json:"allow_regex"`
	Priority        int        `db:"priority" json:"priority"`
	ManualInclude   []string   `db:"manual_include" json:"manual_include"`
	ManualExclude   []string   `db:"manual_exclude" json:"manual_exclude"`
	ActiveVersionID *string    `db:"active_version_id" json:"active_version_id"`
	LastFetchAt     *time.Time `db:"last_fetch_at" json:"last_fetch_at"`
	Version         int64      `db:"version" json:"version"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}

// RuleSource is one origin of rules inside a rule set.
type RuleSource struct {
	ID             string    `db:"id" json:"id"`
	RuleSetID      string    `db:"rule_set_id" json:"rule_set_id"`
	Name           string    `db:"name" json:"name"`
	Type           string    `db:"type" json:"type"`
	URL            string    `db:"url" json:"url"`
	Repo           string    `db:"repo" json:"repo"`
	Ref            string    `db:"ref" json:"ref"`
	Path           string    `db:"path" json:"path"`
	Mode           string    `db:"mode" json:"mode"`
	ExpectedSHA256 string    `db:"expected_sha256" json:"expected_sha256"`
	Enabled        bool      `db:"enabled" json:"enabled"`
	ETag           string    `db:"etag" json:"etag"`
	LastModified   string    `db:"last_modified" json:"last_modified"`
	Version        int64     `db:"version" json:"version"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
}

// RuleSetVersion is an immutable normalized snapshot of a rule set.
type RuleSetVersion struct {
	ID             string         `db:"id" json:"id"`
	RuleSetID      string         `db:"rule_set_id" json:"rule_set_id"`
	Sequence       int64          `db:"sequence" json:"sequence"`
	ContentHash    string         `db:"content_hash" json:"content_hash"`
	Counts         map[string]any `db:"counts" json:"counts"`
	Status         string         `db:"status" json:"status"`
	SourceManifest map[string]any `db:"source_manifest" json:"source_manifest"`
	Warnings       []string       `db:"warnings" json:"warnings"`
	CreatedAt      time.Time      `db:"created_at" json:"created_at"`
}

// Service is a managed logical service.
type Service struct {
	ID             string         `db:"id" json:"id"`
	Name           string         `db:"name" json:"name"`
	Slug           string         `db:"slug" json:"slug"`
	Description    string         `db:"description" json:"description"`
	Enabled        bool           `db:"enabled" json:"enabled"`
	RuleSetID      *string        `db:"rule_set_id" json:"rule_set_id"`
	IngressGroupID *string        `db:"ingress_group_id" json:"ingress_group_id"`
	EgressGroupID  *string        `db:"egress_group_id" json:"egress_group_id"`
	RoutePolicyID  *string        `db:"route_policy_id" json:"route_policy_id"`
	AllowedPorts   []int32        `db:"allowed_ports" json:"allowed_ports"`
	UDPMode        string         `db:"udp_mode" json:"udp_mode"`
	DNSTTL         int            `db:"dns_ttl" json:"dns_ttl"`
	Priority       int            `db:"priority" json:"priority"`
	Notes          string         `db:"notes" json:"notes"`
	Probe          map[string]any `db:"probe" json:"probe"`
	Version        int64          `db:"version" json:"version"`
	CreatedAt      time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at" json:"updated_at"`
}

// Revision is an immutable compiled configuration snapshot.
type Revision struct {
	ID          string         `db:"id" json:"id"`
	Sequence    int64          `db:"sequence" json:"sequence"`
	State       string         `db:"state" json:"state"`
	ModelHash   string         `db:"model_hash" json:"model_hash"`
	Manifest    map[string]any `db:"manifest" json:"manifest"`
	Summary     map[string]any `db:"summary" json:"summary"`
	Error       string         `db:"error" json:"error"`
	CreatedBy   *string        `db:"created_by" json:"created_by"`
	ActivatedAt *time.Time     `db:"activated_at" json:"activated_at"`
	CreatedAt   time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time      `db:"updated_at" json:"updated_at"`
}

// NodeDeployment tracks rollout of one revision to one node.
type NodeDeployment struct {
	ID          string     `db:"id" json:"id"`
	NodeID      string     `db:"node_id" json:"node_id"`
	NodeName    string     `db:"node_name" json:"node_name"`
	RevisionID  string     `db:"revision_id" json:"revision_id"`
	State       string     `db:"state" json:"state"`
	Wave        int        `db:"wave" json:"wave"`
	ErrorCode   string     `db:"error_code" json:"error_code"`
	ErrorDetail string     `db:"error_detail" json:"error_detail"`
	StartedAt   time.Time  `db:"started_at" json:"started_at"`
	FinishedAt  *time.Time `db:"finished_at" json:"finished_at"`
}

// HealthSample is one observation.
type HealthSample struct {
	ID         int64     `db:"id" json:"id"`
	NodeID     *string   `db:"node_id" json:"node_id"`
	ServiceID  *string   `db:"service_id" json:"service_id"`
	Kind       string    `db:"kind" json:"kind"`
	Success    bool      `db:"success" json:"success"`
	LatencyMs  int       `db:"latency_ms" json:"latency_ms"`
	ErrorCode  string    `db:"error_code" json:"error_code"`
	Detail     string    `db:"detail" json:"detail"`
	ObservedAt time.Time `db:"observed_at" json:"observed_at"`
}

// Event is an operator-facing timeline entry.
type Event struct {
	ID        int64          `db:"id" json:"id"`
	Level     string         `db:"level" json:"level"`
	Component string         `db:"component" json:"component"`
	NodeID    *string        `db:"node_id" json:"node_id"`
	Code      string         `db:"code" json:"code"`
	Message   string         `db:"message" json:"message"`
	Data      map[string]any `db:"data" json:"data"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
}

// AuditEvent records every mutating action.
type AuditEvent struct {
	ID         int64          `db:"id" json:"id"`
	Actor      string         `db:"actor" json:"actor"`
	Action     string         `db:"action" json:"action"`
	ObjectType string         `db:"object_type" json:"object_type"`
	ObjectID   string         `db:"object_id" json:"object_id"`
	RequestID  string         `db:"request_id" json:"request_id"`
	BeforeJSON map[string]any `db:"before_json" json:"before"`
	AfterJSON  map[string]any `db:"after_json" json:"after"`
	CreatedAt  time.Time      `db:"created_at" json:"created_at"`
}

// User is a panel account.
type User struct {
	ID           string     `db:"id" json:"id"`
	Email        string     `db:"email" json:"email"`
	PasswordHash string     `db:"password_hash" json:"-"`
	Role         string     `db:"role" json:"role"`
	DisplayName  string     `db:"display_name" json:"display_name"`
	TOTPSecret   []byte     `db:"totp_secret_encrypted" json:"-"`
	TOTPEnabled  bool       `db:"totp_enabled" json:"totp_enabled"`
	FailedLogins int        `db:"failed_logins" json:"-"`
	LockedUntil  *time.Time `db:"locked_until" json:"-"`
	DisabledAt   *time.Time `db:"disabled_at" json:"disabled_at"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
}

// DeviceProfile is a downloadable client setup profile.
type DeviceProfile struct {
	ID        string         `db:"id" json:"id"`
	Name      string         `db:"name" json:"name"`
	Type      string         `db:"type" json:"type"`
	Config    map[string]any `db:"config" json:"config"`
	RevokedAt *time.Time     `db:"revoked_at" json:"revoked_at"`
	Version   int64          `db:"version" json:"version"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
}
