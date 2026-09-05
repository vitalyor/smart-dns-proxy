package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"smartdns/panel/internal/store"
)

// Instructions are stored per platform as Markdown and edited from the panel, so
// a wording fix does not need a rebuild. Personal values are substituted at
// render time, which is why the text is generic but what the person reads is not.
const (
	phDoHURL     = "{{doh_url}}"
	phDoTHost    = "{{dot_host}}"
	phDeviceName = "{{device_name}}"
	phIngressV4  = "{{ingress_ipv4}}"
)

// md renders Markdown to HTML. Raw HTML in the source stays escaped: the text is
// operator-authored, but there is no reason for it to be able to inject markup.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

type instructionRow struct {
	Platform  string    `db:"platform"`
	Body      string    `db:"body"`
	UpdatedAt time.Time `db:"updated_at"`
}

// listInstructions returns every platform, seeding a platform that has never
// been edited with the built-in text so the editor is never blank.
func (s *Server) listInstructions(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := store.Many[instructionRow](ctx, s.DB, `SELECT * FROM instructions`)
	if err != nil {
		return err
	}
	have := map[string]instructionRow{}
	for _, x := range rows {
		have[x.Platform] = x
	}
	out := make([]map[string]any, 0, len(deviceTypes))
	for _, p := range deviceTypes {
		body, edited := have[p].Body, true
		if _, ok := have[p]; !ok || strings.TrimSpace(body) == "" {
			body, edited = defaultInstruction(p), false
		}
		out = append(out, map[string]any{"platform": p, "body": body, "edited": edited,
			"label": platformLabel(p)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":        out,
		"version":      s.instructionsVersion(ctx),
		"placeholders": []string{phDoHURL, phDoTHost, phDeviceName, phIngressV4},
	})
	return nil
}

func platformLabel(p string) string {
	switch p {
	case "android_dot":
		return "Android"
	case "apple_doh":
		return "iPhone, iPad, Mac"
	case "apple_dot":
		return "Apple (DoT)"
	case "windows_doh":
		return "Windows"
	case "router":
		return "Роутер"
	default:
		return "Другое"
	}
}

func (s *Server) putInstruction(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	p := r.PathValue("platform")
	if !contains(deviceTypes, p) {
		return badRequest("неизвестная платформа: %s", p)
	}
	if _, err := s.DB.Exec(r.Context(), `
		INSERT INTO instructions (platform, body) VALUES ($1,$2)
		ON CONFLICT (platform) DO UPDATE SET body=EXCLUDED.body, updated_at=now()`, p, req.Body); err != nil {
		return err
	}
	s.audit(r.Context(), r, "instructions.updated", "instructions", p, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.instructionsVersion(r.Context())})
	return nil
}

// instructionsVersion changes whenever any text or image changes, so the
// subscription page can tell "same as cached" from "refetch" without a restart.
func (s *Server) instructionsVersion(ctx contextT) string {
	var a, b *time.Time
	_ = s.DB.QueryRow(ctx, `SELECT max(updated_at) FROM instructions`).Scan(&a)
	_ = s.DB.QueryRow(ctx, `SELECT max(created_at) FROM instruction_assets`).Scan(&b)
	h := sha256.New()
	if a != nil {
		fmt.Fprint(h, a.UnixNano())
	}
	if b != nil {
		fmt.Fprint(h, b.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// previewInstruction renders unsaved text with sample values, so the operator
// sees what a person will see instead of editing blind.
func (s *Server) previewInstruction(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ep := s.dnsEndpoints(r.Context())
	dohHost, _ := ep["doh_hostname"].(string)
	dotHost, _ := ep["dot_hostname"].(string)
	dohPath, _ := ep["doh_path"].(string)
	if dohPath == "" {
		dohPath = "/dns-query"
	}
	dohURL := "https://" + dohHost + strings.TrimRight(dohPath, "/") + "/ПРИМЕР-ТОКЕНА"
	var v4 []string
	if raw, ok := ep["ingress_ipv4"].([]string); ok {
		v4 = raw
	}
	rep := strings.NewReplacer(
		phDoHURL, dohURL,
		phDoTHost, dotHost,
		phDeviceName, "iPhone Виталия",
		phIngressV4, strings.Join(v4, ", "),
		"](asset/", "](/api/v1/instruction-assets/",
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(rep.Replace(req.Body)), &buf); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": buf.String()})
	return nil
}

// --- assets ------------------------------------------------------------------

func (s *Server) uploadAsset(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return badRequest("не удалось прочитать файл")
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return badRequest("выберите файл")
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return err
	}
	ct := hdr.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return badRequest("можно загружать только изображения")
	}
	var id string
	if err := s.DB.QueryRow(r.Context(), `
		INSERT INTO instruction_assets (filename, content_type, bytes, size)
		VALUES ($1,$2,$3,$4) RETURNING id::text`,
		hdr.Filename, ct, body, len(body)).Scan(&id); err != nil {
		return err
	}
	s.audit(r.Context(), r, "instructions.asset.uploaded", "asset", id, nil,
		map[string]any{"name": hdr.Filename, "size": len(body)})
	// The markdown reference form. The subscription page rewrites it to its own
	// path, so the person's browser never talks to the panel.
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "markdown": "![](asset/" + id + ")"})
	return nil
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		CT    string `db:"content_type"`
		Bytes []byte `db:"bytes"`
	}
	a, err := store.One[row](r.Context(), s.DB,
		`SELECT content_type, bytes FROM instruction_assets WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return notFound("asset")
	}
	w.Header().Set("Content-Type", a.CT)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(a.Bytes)
	return nil
}

// --- rendering ---------------------------------------------------------------

// renderInstruction produces the HTML one person sees for one device: the
// platform text with their own addresses substituted and asset links pointed at
// whoever is serving the page.
func (s *Server) renderInstruction(ctx contextT, p store.DeviceProfile, assetBase string) (string, error) {
	var body string
	_ = s.DB.QueryRow(ctx, `SELECT body FROM instructions WHERE platform=$1`, p.Type).Scan(&body)
	if strings.TrimSpace(body) == "" {
		body = defaultInstruction(p.Type)
	}
	dohURL, dotHost, v4 := deviceAddressesFull(p)
	rep := strings.NewReplacer(
		phDoHURL, dohURL,
		phDoTHost, dotHost,
		phDeviceName, p.Name,
		phIngressV4, strings.Join(v4, ", "),
		"](asset/", "]("+assetBase,
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(rep.Replace(body)), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// subDeviceInstructions serves the rendered instruction for one device.
func (s *Server) subDeviceInstructions(w http.ResponseWriter, r *http.Request) error {
	sub, err := s.subscriberByShortID(r)
	if err != nil {
		return notFound("subscriber")
	}
	p, err := store.One[store.DeviceProfile](r.Context(), s.DB,
		`SELECT * FROM device_profiles WHERE id=$1 AND subscriber_id=$2`,
		r.PathValue("device_id"), sub.ID)
	if err != nil {
		return notFound("device")
	}
	base := r.URL.Query().Get("asset_base")
	if base == "" {
		base = "asset/"
	}
	html, err := s.renderInstruction(r.Context(), p, base)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"platform": p.Type, "label": platformLabel(p.Type),
		"html": html, "version": s.instructionsVersion(r.Context()),
	})
	return nil
}

// subAsset serves an instruction image to the subscription page.
func (s *Server) subAsset(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.subscriberByShortID(r); err != nil {
		return notFound("subscriber")
	}
	return s.serveAsset(w, r)
}
