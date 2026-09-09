package proxy

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"smartdns/shared/model"
)

// Курсор — единственное, на чём держится живая лента: панель дочитывает по
// номеру. Если snapshot вернёт уже показанное или пропустит новое, лента
// задвоится или потеряет строку, а заметить это глазами почти нельзя.
func TestConnRingCursor(t *testing.T) {
	r := newConnRing(3)
	for _, sni := range []string{"a", "b"} {
		r.add(ConnEntry{SNI: sni})
	}
	seq, got := r.snapshot(0)
	if seq != 2 || len(got) != 2 {
		t.Fatalf("ожидались 2 записи и номер 2, получено %d и %d", len(got), seq)
	}

	// Дочитывание с курсора отдаёт только новое.
	r.add(ConnEntry{SNI: "c"})
	seq, got = r.snapshot(2)
	if len(got) != 1 || got[0].SNI != "c" || seq != 3 {
		t.Fatalf("с курсора 2 ожидалась одна запись «c», получено %+v (номер %d)", got, seq)
	}

	// Кольцо на три: четвёртая вытесняет первую, номера не сбрасываются.
	r.add(ConnEntry{SNI: "d"})
	seq, got = r.snapshot(0)
	if len(got) != 3 || got[0].SNI != "b" || seq != 4 {
		t.Fatalf("после вытеснения ожидались b,c,d и номер 4, получено %+v (номер %d)", got, seq)
	}
}

// Журнал соединений хранит имена сайтов и адреса устройств. Пока панель не
// попросила уровень debug, он обязан молчать — и не копить записи впрок.
func TestConnLogSilentUntilDebug(t *testing.T) {
	p := New(&model.NodeConfig{}, &tls.Config{})
	p.note(ConnEntry{SNI: "example.com", Result: "ok"})
	if _, got := p.connLog.snapshot(0); len(got) != 0 {
		t.Fatalf("без отладки кольцо должно быть пустым, в нём %d записей", len(got))
	}

	rec := httptest.NewRecorder()
	p.LogHandler()(rec, httptest.NewRequest(http.MethodGet, "/log", nil))
	var off struct {
		Enabled bool        `json:"enabled"`
		Entries []ConnEntry `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&off); err != nil {
		t.Fatal(err)
	}
	if off.Enabled {
		t.Fatal("выключенная отладка не должна объявлять себя включённой")
	}

	// Панель подняла уровень до debug — записи пошли.
	p.Apply(&model.NodeConfig{LogLevel: "debug"})
	p.note(ConnEntry{SNI: "example.com", Result: "ok"})
	rec = httptest.NewRecorder()
	p.LogHandler()(rec, httptest.NewRequest(http.MethodGet, "/log", nil))
	var on struct {
		Enabled bool        `json:"enabled"`
		Entries []ConnEntry `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&on); err != nil {
		t.Fatal(err)
	}
	if !on.Enabled || len(on.Entries) != 1 || on.Entries[0].SNI != "example.com" {
		t.Fatalf("после включения отладки ожидалась одна запись, получено %+v", on)
	}
}
