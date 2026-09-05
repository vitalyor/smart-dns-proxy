package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/store"
)

func (s *Server) listDeviceProfiles(w http.ResponseWriter, r *http.Request) error {
	rows, err := store.Many[store.DeviceProfile](r.Context(), s.DB,
		`SELECT id, name, type, config, revoked_at, version, created_at FROM device_profiles ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    rows,
		"defaults": s.dnsEndpoints(r.Context()),
	})
	return nil
}

func (s *Server) dnsEndpoints(ctx contextT) map[string]any {
	type row struct {
		Name string  `db:"name"`
		IPv4 *string `db:"public_ipv4"`
		IPv6 *string `db:"public_ipv6"`
	}
	rows, _ := store.Many[row](ctx, s.DB,
		`SELECT name, public_ipv4, public_ipv6 FROM nodes WHERE role='ingress' AND status <> 'disabled' ORDER BY name`)
	var v4, v6 []string
	for _, r := range rows {
		if r.IPv4 != nil {
			v4 = append(v4, *r.IPv4)
		}
		if r.IPv6 != nil {
			v6 = append(v6, *r.IPv6)
		}
	}
	return map[string]any{
		"doh_hostname": getSetting(ctx, s.DB, "doh_hostname", ""),
		"dot_hostname": getSetting(ctx, s.DB, "dot_hostname", ""),
		"doh_path":     getSetting(ctx, s.DB, "doh_path", "/dns-query"),
		"ingress_ipv4": v4,
		"ingress_ipv6": v6,
		"access_mode":  getSetting(ctx, s.DB, "dns_access_mode", "allowlist"),
	}
}

// deviceTypes are the platforms a profile can target.
var deviceTypes = []string{"android_dot", "apple_doh", "apple_dot", "windows_doh", "router", "plain"}

// buildDeviceConfig assembles the stored config for a new device and mints its
// DoH path token where the platform can carry one. DoH profiles get a unique
// path token so the resolver is not open to the whole Internet; Android Private
// DNS (DoT) cannot carry such a token — that limitation is surfaced to the
// operator, not hidden. Shared by the operator page and the subscription API.
func (s *Server) buildDeviceConfig(ctx contextT, typ string) (map[string]any, string, string) {
	cfg := map[string]any{"endpoints": s.dnsEndpoints(ctx)}
	var token, tokenHash string
	switch typ {
	case "apple_doh", "windows_doh", "router":
		token = auth.RandomToken(18)
		tokenHash = hashToken(token)
		cfg["path_token"] = token
	case "android_dot":
		cfg["warning"] = "Android Private DNS не передаёт токен. Для этого профиля резолвер работает в режиме restricted-public-dot: строгие лимиты запросов вместо персональной аутентификации."
	}
	return cfg, token, tokenHash
}

type deviceProfileRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Server) createDeviceProfile(w http.ResponseWriter, r *http.Request) error {
	var req deviceProfileRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if !contains(deviceTypes, req.Type) {
		return badRequest("недопустимый тип профиля")
	}
	if strings.TrimSpace(req.Name) == "" {
		return badRequest("укажите название профиля")
	}
	cfg, token, tokenHash := s.buildDeviceConfig(r.Context(), req.Type)
	b, _ := json.Marshal(cfg)
	p, err := store.One[store.DeviceProfile](r.Context(), s.DB, `
		INSERT INTO device_profiles (name, type, config, token_hash) VALUES ($1,$2,$3,$4)
		RETURNING id, name, type, config, revoked_at, version, created_at`,
		req.Name, req.Type, b, tokenHash)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "device_profile.created", "device_profile", p.ID, nil,
		map[string]any{"name": req.Name, "type": req.Type})
	writeJSON(w, http.StatusCreated, map[string]any{"profile": p, "token": token})
	return nil
}

func (s *Server) deleteDeviceProfile(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM device_profiles WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("profile")
	}
	s.audit(r.Context(), r, "device_profile.deleted", "device_profile", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

// renderDeviceArtifact produces the platform-specific setup file for a profile:
// a .mobileconfig for Apple, Markdown instructions elsewhere. Shared by the
// operator download and the subscription page, so both stay identical.
func renderDeviceArtifact(p store.DeviceProfile) (contentType, filename string, body []byte) {
	dohURL, dotHost, v4 := deviceAddressesFull(p)
	switch p.Type {
	case "apple_doh", "apple_dot":
		return "application/x-apple-aspen-config", safeName(p.Name) + ".mobileconfig",
			[]byte(appleMobileconfig(p, dohURL, dotHost, v4))
	default:
		return "text/markdown; charset=utf-8", safeName(p.Name) + ".md",
			[]byte(instructions(p, dohURL, dotHost, v4))
	}
}

// deviceAddressesFull unpacks the personal endpoints stored in a profile.
func deviceAddressesFull(p store.DeviceProfile) (dohURL, dotHost string, v4 []string) {
	endpoints, _ := p.Config["endpoints"].(map[string]any)
	dohHost, _ := endpoints["doh_hostname"].(string)
	dotHost, _ = endpoints["dot_hostname"].(string)
	dohPath, _ := endpoints["doh_path"].(string)
	token, _ := p.Config["path_token"].(string)
	if dohPath == "" {
		dohPath = "/dns-query"
	}
	if dohHost != "" {
		dohURL = "https://" + dohHost + strings.TrimRight(dohPath, "/")
		if token != "" {
			dohURL += "/" + token
		}
	}
	if raw, ok := endpoints["ingress_ipv4"].([]any); ok {
		for _, x := range raw {
			if s, ok := x.(string); ok {
				v4 = append(v4, s)
			}
		}
	}
	return dohURL, dotHost, v4
}

// downloadDeviceProfile renders the platform-specific setup artifact.
func (s *Server) downloadDeviceProfile(w http.ResponseWriter, r *http.Request) error {
	p, err := store.One[store.DeviceProfile](r.Context(), s.DB,
		`SELECT * FROM device_profiles WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	ct, name, body := renderDeviceArtifact(p)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(body)
	return nil
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "smartdns-profile"
	}
	return out
}

func appleMobileconfig(p store.DeviceProfile, dohURL, dotHost string, ingressV4 []string) string {
	uuid1, uuid2 := auth.RandomToken(16), auth.RandomToken(16)
	var settings string
	if p.Type == "apple_doh" && dohURL != "" {
		settings = fmt.Sprintf(`
				<key>DNSProtocol</key><string>HTTPS</string>
				<key>ServerURL</key><string>%s</string>`, xmlEscape(dohURL))
	} else {
		settings = fmt.Sprintf(`
				<key>DNSProtocol</key><string>TLS</string>
				<key>ServerName</key><string>%s</string>`, xmlEscape(dotHost))
	}
	// Listing every ingress address lets the device fail over on its own when
	// one node is down — the Google/Cloudflare dual-address model, applied to
	// however many ingress nodes the group has.
	if len(ingressV4) > 0 {
		var addrs strings.Builder
		for _, ip := range ingressV4 {
			fmt.Fprintf(&addrs, "\n\t\t\t\t\t<string>%s</string>", xmlEscape(ip))
		}
		settings += fmt.Sprintf(`
				<key>ServerAddresses</key>
				<array>%s
				</array>`, addrs.String())
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>DNSSettings</key>
			<dict>%s
			</dict>
			<key>PayloadDescription</key><string>SmartDNS encrypted DNS configuration</string>
			<key>PayloadDisplayName</key><string>%s</string>
			<key>PayloadIdentifier</key><string>net.smartdns.dnsSettings.%s</string>
			<key>PayloadType</key><string>com.apple.dnsSettings.managed</string>
			<key>PayloadUUID</key><string>%s</string>
			<key>PayloadVersion</key><integer>1</integer>
		</dict>
	</array>
	<key>PayloadDisplayName</key><string>%s</string>
	<key>PayloadIdentifier</key><string>net.smartdns.profile.%s</string>
	<key>PayloadRemovalDisallowed</key><false/>
	<key>PayloadType</key><string>Configuration</string>
	<key>PayloadUUID</key><string>%s</string>
	<key>PayloadVersion</key><integer>1</integer>
</dict>
</plist>
`, settings, xmlEscape(p.Name), p.ID, uuid1, xmlEscape(p.Name), p.ID, uuid2)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func instructions(p store.DeviceProfile, dohURL, dotHost string, v4 []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", p.Name)
	switch p.Type {
	case "android_dot":
		fmt.Fprintf(&b, "## Android — Private DNS\n\n")
		fmt.Fprintf(&b, "1. Настройки → Сеть и интернет → Частный DNS.\n")
		fmt.Fprintf(&b, "2. Выберите «Имя хоста поставщика частного DNS».\n")
		fmt.Fprintf(&b, "3. Введите: `%s`\n\n", dotHost)
		fmt.Fprintf(&b, "> Android принимает одно имя. Оно резолвится во все адреса входных нод (multi-A),\n")
		fmt.Fprintf(&b, "> поэтому при падении одной ноды устройство само переходит на другую.\n")
		if len(v4) > 1 {
			fmt.Fprintf(&b, ">\n> Адреса за этим именем: %s\n", strings.Join(v4, ", "))
		}
		fmt.Fprintf(&b, "> Android не передаёт токен доступа, поэтому резолвер защищён строгими лимитами запросов.\n")
	case "windows_doh":
		fmt.Fprintf(&b, "## Windows 11 — DNS over HTTPS\n\n")
		fmt.Fprintf(&b, "1. Параметры → Сеть и Интернет → выберите адаптер → Изменить назначение DNS-серверов.\n")
		fmt.Fprintf(&b, "2. Вручную, IPv4 = вкл, предпочитаемый DNS: `%s`\n", firstOr(v4, "<адрес ingress>"))
		if len(v4) > 1 {
			fmt.Fprintf(&b, "   альтернативный DNS: `%s` — Windows переключится на него, если первый молчит.\n", v4[1])
		}
		fmt.Fprintf(&b, "3. Шифрование DNS: «Только зашифрованный (DNS over HTTPS)», шаблон: `%s`\n\n", dohURL)
		fmt.Fprintf(&b, "> Windows 10 не поддерживает произвольный DoH-шаблон в UI: используйте роутер или обычный DNS в доверенной сети.\n")
	case "router":
		fmt.Fprintf(&b, "## Роутер / OpenWrt\n\n```sh\n")
		fmt.Fprintf(&b, "opkg update && opkg install https-dns-proxy\n")
		fmt.Fprintf(&b, "uci set https-dns-proxy.@https-dns-proxy[0].resolver_url='%s'\n", dohURL)
		fmt.Fprintf(&b, "uci commit https-dns-proxy && /etc/init.d/https-dns-proxy restart\n```\n\n")
		fmt.Fprintf(&b, "Альтернатива — DoT через stubby, hostname `%s`.\n", dotHost)
	default:
		fmt.Fprintf(&b, "## Обычный DNS\n\n")
		for _, a := range v4 {
			fmt.Fprintf(&b, "- %s\n", a)
		}
		fmt.Fprintf(&b, "\n> Обычный DNS передаётся без шифрования. Используйте его только в доверенной сети\n")
		fmt.Fprintf(&b, "> или там, где устройство не поддерживает DoH/DoT.\n")
	}
	fmt.Fprintf(&b, "\n## Проверка\n\n```sh\n")
	fmt.Fprintf(&b, "dig +short <управляемый-домен>   # должен вернуть IP входной ноды\n")
	fmt.Fprintf(&b, "dig +short example.org           # должен вернуть настоящий IP\n")
	fmt.Fprintf(&b, "curl -sI https://<управляемый-домен> | head -1\n```\n")
	fmt.Fprintf(&b, "\n## Известные ограничения\n\n")
	fmt.Fprintf(&b, "- HTTP/3 (QUIC, UDP/443) по умолчанию отключён: браузер откатывается на TCP.\n")
	fmt.Fprintf(&b, "- Сервисы с обязательным Encrypted ClientHello не поддерживаются.\n")
	fmt.Fprintf(&b, "- Два DNS-адреса в настройках ОС не гарантируют переключение primary→secondary.\n")
	return b.String()
}

func firstOr(v []string, def string) string {
	if len(v) > 0 {
		return v[0]
	}
	return def
}
