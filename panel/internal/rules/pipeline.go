// Package rules implements the rule-set update pipeline:
// fetch -> parse -> normalize -> validate -> diff -> candidate -> approve.
// A failure at any step leaves the previously active version untouched.
package rules

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"smartdns/panel/internal/fetcher"
	"smartdns/panel/internal/store"
	"smartdns/shared/domainset"
)

//go:embed presets
var presetFS embed.FS

// Presets lists the rule lists shipped with the project.
func Presets() map[string]string {
	out := map[string]string{}
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return out
	}
	for _, e := range entries {
		b, err := presetFS.ReadFile("presets/" + e.Name())
		if err == nil {
			out[strings.TrimSuffix(e.Name(), ".txt")] = string(b)
		}
	}
	return out
}

// Thresholds decide when a change needs a human.
type Thresholds struct {
	MaxRemovedPercent int
	MaxAdded          int
}

// DefaultThresholds match the specification defaults.
var DefaultThresholds = Thresholds{MaxRemovedPercent: 30, MaxAdded: 1000}

// BuildResult is the outcome of one pipeline run.
type BuildResult struct {
	VersionID     string          `json:"version_id"`
	Sequence      int64           `json:"sequence"`
	ContentHash   string          `json:"content_hash"`
	Status        string          `json:"status"`
	Unchanged     bool            `json:"unchanged"`
	Counts        map[string]int  `json:"counts"`
	Added         int             `json:"added"`
	Removed       int             `json:"removed"`
	AddedSample   []string        `json:"added_sample"`
	RemovedSample []string        `json:"removed_sample"`
	Warnings      []string        `json:"warnings"`
	Sources       []SourceOutcome `json:"sources"`
}

// SourceOutcome reports what happened to one source.
type SourceOutcome struct {
	SourceID   string `json:"source_id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	Entries    int    `json:"entries"`
	Skipped    int    `json:"skipped"`
	Hash       string `json:"content_hash"`
	Error      string `json:"error,omitempty"`
}

// Builder runs the pipeline against the database.
type Builder struct {
	DB           *store.DB
	Fetch        *fetcher.Client
	Thresholds   Thresholds
	AllowPrivate bool
	Decrypt      func([]byte) (string, error)
}

// ErrAllSourcesFailed means nothing usable was downloaded; the active version
// must stay in place.
var ErrAllSourcesFailed = errors.New("every enabled source failed; the active rule set is unchanged")

// Build fetches every enabled source and creates a candidate version.
func (b *Builder) Build(ctx context.Context, ruleSetID string) (*BuildResult, error) {
	rs, err := store.One[store.RuleSet](ctx, b.DB, `SELECT * FROM rule_sets WHERE id=$1`, ruleSetID)
	if err != nil {
		return nil, err
	}
	sources, err := store.Many[store.RuleSource](ctx, b.DB,
		`SELECT * FROM rule_sources WHERE rule_set_id=$1 AND enabled ORDER BY created_at`, ruleSetID)
	if err != nil {
		return nil, err
	}

	res := &BuildResult{Counts: map[string]int{}, Warnings: []string{}}
	opts := domainset.ParseOptions{AllowRegex: rs.AllowRegex}
	var includes, excludes []domainset.Entry
	okSources, failedSources := 0, 0
	manifest := map[string]any{"sources": []any{}, "built_at": time.Now().UTC()}

	for _, src := range sources {
		outcome := SourceOutcome{SourceID: src.ID, Name: sourceLabel(src), Type: src.Type, Mode: src.Mode}
		body, hash, httpStatus, err := b.load(ctx, src)
		outcome.HTTPStatus = httpStatus
		outcome.Hash = hash
		if err != nil {
			outcome.Status, outcome.Error = "failed", err.Error()
			failedSources++
			b.recordFetch(ctx, src.ID, "failed", httpStatus, hash, 0, 0, err.Error())
			res.Warnings = append(res.Warnings, fmt.Sprintf("источник %s: %v", outcome.Name, err))
			res.Sources = append(res.Sources, outcome)
			continue
		}
		parsed, err := domainset.ParseLines(body, opts)
		if err != nil {
			outcome.Status, outcome.Error = "failed", err.Error()
			failedSources++
			b.recordFetch(ctx, src.ID, "failed", httpStatus, hash, int64(len(body)), 0, err.Error())
			res.Sources = append(res.Sources, outcome)
			continue
		}
		outcome.Status = "ok"
		outcome.Entries = len(parsed.Entries)
		outcome.Skipped = parsed.Skipped
		okSources++
		if src.Mode == "exclude" {
			excludes = append(excludes, parsed.Entries...)
		} else {
			includes = append(includes, parsed.Entries...)
		}
		for _, wrn := range parsed.Warnings {
			if len(res.Warnings) < 50 {
				res.Warnings = append(res.Warnings, outcome.Name+": "+wrn)
			}
		}
		b.recordFetch(ctx, src.ID, "ok", httpStatus, hash, int64(len(body)), len(parsed.Entries), "")
		res.Sources = append(res.Sources, outcome)
		manifest["sources"] = append(manifest["sources"].([]any), map[string]any{
			"id": src.ID, "type": src.Type, "url": src.URL, "repo": src.Repo,
			"ref": src.Ref, "path": src.Path, "hash": hash, "entries": len(parsed.Entries),
		})
	}

	// Manual lists are applied last so an operator can always override a source.
	manualInc, _ := domainset.ParseLines(strings.Join(rs.ManualInclude, "\n"), opts)
	manualExc, _ := domainset.ParseLines(strings.Join(rs.ManualExclude, "\n"), opts)
	includes = append(includes, manualInc.Entries...)
	excludes = append(excludes, manualExc.Entries...)

	if okSources == 0 && failedSources > 0 && len(manualInc.Entries) == 0 {
		return res, ErrAllSourcesFailed
	}

	effective := domainset.Merge(includes, excludes)
	res.ContentHash = domainset.Hash(effective)
	res.Counts = domainset.Counts(effective)

	var prev []domainset.Entry
	var prevHash string
	if rs.ActiveVersionID != nil {
		prevHash, _ = store.Value[string](ctx, b.DB, `SELECT content_hash FROM rule_set_versions WHERE id=$1`, *rs.ActiveVersionID)
		prev, _ = b.entriesOf(ctx, *rs.ActiveVersionID)
	}
	added, removed := domainset.Diff(prev, effective)
	res.Added, res.Removed = len(added), len(removed)
	res.AddedSample = sample(added, 25)
	res.RemovedSample = sample(removed, 25)

	if prevHash != "" && prevHash == res.ContentHash {
		res.Unchanged = true
		res.Status = "unchanged"
		_, _ = b.DB.Exec(ctx, `UPDATE rule_sets SET last_fetch_at=now() WHERE id=$1`, ruleSetID)
		return res, nil
	}

	// A non-empty list must never be silently replaced by an empty one.
	suspicious := false
	if len(prev) > 0 && len(effective) == 0 {
		suspicious = true
		res.Warnings = append(res.Warnings, "новый список пуст, хотя предыдущий содержал записи — вероятно, источник сломан")
	}
	th := b.Thresholds
	if th.MaxAdded == 0 {
		th = DefaultThresholds
	}
	if len(prev) > 0 && len(removed)*100 > len(prev)*th.MaxRemovedPercent {
		suspicious = true
		res.Warnings = append(res.Warnings, fmt.Sprintf("удалено %d из %d записей (>%d%%) — требуется подтверждение",
			len(removed), len(prev), th.MaxRemovedPercent))
	}
	if len(added) > th.MaxAdded {
		suspicious = true
		res.Warnings = append(res.Warnings, fmt.Sprintf("добавлено %d записей (>%d) — требуется подтверждение", len(added), th.MaxAdded))
	}
	if failedSources > 0 {
		suspicious = true
	}

	status := "awaiting_approval"
	switch {
	case len(prev) == 0 && len(effective) > 0 && !suspicious:
		// Nothing to lose on the first non-empty build, so activate it: an
		// approval step here would only block the very first setup.
		status = "active"
	case rs.UpdateMode == "auto_apply" && !suspicious:
		status = "active"
	}
	res.Status = status

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(sequence),0)+1 FROM rule_set_versions WHERE rule_set_id=$1`, ruleSetID).Scan(&seq); err != nil {
		return res, err
	}
	countsJSON, _ := json.Marshal(res.Counts)
	manifestJSON, _ := json.Marshal(manifest)
	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO rule_set_versions (rule_set_id, sequence, content_hash, counts, status, source_manifest, warnings)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		ruleSetID, seq, res.ContentHash, countsJSON, status, manifestJSON, nonNilStrings(res.Warnings)).Scan(&versionID); err != nil {
		return res, err
	}
	rows := make([][]any, 0, len(effective))
	for _, e := range effective {
		rows = append(rows, []any{versionID, string(e.Kind), e.Value})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"rule_entries"},
		[]string{"version_id", "kind", "value"}, pgx.CopyFromRows(rows)); err != nil {
		return res, err
	}
	if status == "active" {
		if _, err := tx.Exec(ctx,
			`UPDATE rule_set_versions SET status='superseded' WHERE rule_set_id=$1 AND id<>$2 AND status='active'`,
			ruleSetID, versionID); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE rule_sets SET active_version_id=$2, last_fetch_at=now(), updated_at=now() WHERE id=$1`,
			ruleSetID, versionID); err != nil {
			return res, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE rule_sets SET last_fetch_at=now() WHERE id=$1`, ruleSetID); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	res.VersionID, res.Sequence = versionID, seq
	slog.Info("rule set candidate built", "rule_set", rs.Name, "sequence", seq,
		"status", status, "entries", len(effective), "added", len(added), "removed", len(removed))
	return res, nil
}

// Approve activates a candidate version.
func (b *Builder) Approve(ctx context.Context, ruleSetID, versionID string) error {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM rule_set_versions WHERE id=$1 AND rule_set_id=$2`,
		versionID, ruleSetID).Scan(&status); err != nil {
		return store.ErrNotFound
	}
	if status == "rejected" {
		return errors.New("эта версия отклонена и не может быть активирована")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE rule_set_versions SET status='superseded' WHERE rule_set_id=$1 AND status='active'`, ruleSetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET status='active' WHERE id=$1`, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE rule_sets SET active_version_id=$2, updated_at=now(), version=version+1 WHERE id=$1`,
		ruleSetID, versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Entries returns the normalized entries of a version.
func (b *Builder) Entries(ctx context.Context, versionID string) ([]domainset.Entry, error) {
	return b.entriesOf(ctx, versionID)
}

func (b *Builder) entriesOf(ctx context.Context, versionID string) ([]domainset.Entry, error) {
	type row struct {
		Kind  string `db:"kind"`
		Value string `db:"value"`
	}
	rows, err := store.Many[row](ctx, b.DB,
		`SELECT kind, value FROM rule_entries WHERE version_id=$1 ORDER BY kind, value`, versionID)
	if err != nil {
		return nil, err
	}
	out := make([]domainset.Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, domainset.Entry{Kind: domainset.Kind(r.Kind), Value: r.Value})
	}
	return out, nil
}

func (b *Builder) load(ctx context.Context, src store.RuleSource) (body, hash string, httpStatus int, err error) {
	switch src.Type {
	case "preset":
		p, ok := Presets()[src.Path]
		if !ok {
			return "", "", 0, fmt.Errorf("встроенный список %q не найден", src.Path)
		}
		return p, domainset.Hash(nil), 0, nil
	case "manual":
		return src.URL, "", 0, nil // manual bodies are stored in the url column
	case "github_repo":
		u, err := fetcher.GitHubRawURL(src.Repo, src.Ref, src.Path)
		if err != nil {
			return "", "", 0, err
		}
		src.URL = u
	}
	if src.URL == "" {
		return "", "", 0, errors.New("у источника не задан URL")
	}
	res, err := b.Fetch.Fetch(ctx, fetcher.Request{
		URL: src.URL, ETag: src.ETag, LastModified: src.LastModified,
		ExpectedSHA256: src.ExpectedSHA256, AllowPrivate: b.AllowPrivate,
	})
	if err != nil {
		if res != nil {
			return "", "", res.StatusCode, err
		}
		return "", "", 0, err
	}
	if res.NotModified {
		// 304 means the upstream content is unchanged; reuse the last body by
		// re-downloading without validators would be wasteful, so we ask the
		// caller to keep the previous version by returning a sentinel.
		res2, err := b.Fetch.Fetch(ctx, fetcher.Request{URL: src.URL, AllowPrivate: b.AllowPrivate})
		if err != nil {
			return "", "", res.StatusCode, err
		}
		res = res2
	}
	_, _ = b.DB.Exec(ctx, `UPDATE rule_sources SET etag=$2, last_modified=$3, updated_at=now() WHERE id=$1`,
		src.ID, res.ETag, res.LastModified)
	return res.Body, res.SHA256, res.StatusCode, nil
}

func (b *Builder) recordFetch(ctx context.Context, sourceID, status string, httpStatus int, hash string, size int64, entries int, errStr string) {
	_, _ = b.DB.Exec(ctx, `INSERT INTO rule_fetches
		(source_id, status, http_status, content_hash, size_bytes, entries, error, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())`, sourceID, status, httpStatus, hash, size, entries, errStr)
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func sourceLabel(s store.RuleSource) string {
	if s.Name != "" {
		return s.Name
	}
	if s.Repo != "" {
		return s.Repo + "@" + s.Ref + ":" + s.Path
	}
	if s.URL != "" {
		return s.URL
	}
	return s.Type
}

func sample(es []domainset.Entry, n int) []string {
	out := make([]string, 0, n)
	for i, e := range es {
		if i >= n {
			break
		}
		out = append(out, string(e.Kind)+":"+e.Value)
	}
	return out
}
