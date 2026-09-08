package proxy

import "testing"

// На 443 имя сравнивалось точно, и DoH по личному имени отбивался как чужой SNI,
// хотя на 853 то же имя работало. Проверяем обе формы и границы шаблона.
func TestDoHMatch(t *testing.T) {
	const doh = "dns.example.net"
	cases := []struct {
		host string
		want bool
		why  string
	}{
		{"dns.example.net", true, "само имя резолвера"},
		{"gjzfw5lg.dns.example.net", true, "личное имя устройства"},
		{"a.b.dns.example.net", false, "два уровня — не наш случай"},
		{".dns.example.net", false, "пустая метка"},
		{"dns.example.net.evil.com", false, "имя лишь начинается с нашего"},
		{"evildns.example.net", false, "приклеено без точки"},
		{"example.net", false, "зона выше резолвера"},
		{"", false, "пусто"},
	}
	for _, c := range cases {
		if got := dohMatch(c.host, doh); got != c.want {
			t.Errorf("dohMatch(%q) = %v, ожидалось %v — %s", c.host, got, c.want, c.why)
		}
	}
}
