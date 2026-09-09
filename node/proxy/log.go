package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
)

// ConnEntry — одно соединение, дошедшее до входа: что за имя, какому сервису
// оно досталось и через какую ноду ушло наружу. DNS-журнал отвечает только на
// вопрос «какое имя спросили»; одного такого вопроса хватает на сотни запросов
// по одному живому соединению, поэтому по нему нельзя понять, что происходит
// сейчас. Здесь — вторая половина картины.
//
// Запись делается в момент решения, а не при закрытии: соединение живёт до
// пяти минут простоя, и ждать его конца значит не увидеть ничего живого.
// Поэтому байтов здесь нет — они известны только в конце и уже посчитаны в
// метриках по сервисам.
//
// ponytail: одна запись на соединение, без обновления по ходу жизни.
type ConnEntry struct {
	Seq     uint64 `json:"seq"`
	TS      int64  `json:"ts"` // unix milliseconds
	Client  string `json:"client"`
	SNI     string `json:"sni"`
	Port    int    `json:"port"`
	Service string `json:"service,omitempty"`
	// Egress — имя ноды выхода, «прямой выход» при выходе со входной ноды,
	// пусто у отказов, до которых выбор ноды не дошёл.
	Egress string `json:"egress,omitempty"`
	// Result — «ok» либо причина отказа: no_egress, sni_not_managed,
	// port_not_allowed, write_failed и прочие из reasonOf.
	Result string `json:"result"`
	MS     int64  `json:"ms"` // от приёма соединения до решения
}

// connRing — кольцо последних соединений с монотонным номером, чтобы панель
// дочитывала его по курсору (?after=<seq>), как и журнал DNS.
type connRing struct {
	mu   sync.Mutex
	buf  []ConnEntry
	seq  uint64
	size int
}

func newConnRing(size int) *connRing { return &connRing{size: size} }

func (r *connRing) add(e ConnEntry) {
	r.mu.Lock()
	r.seq++
	e.Seq = r.seq
	r.buf = append(r.buf, e)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
	r.mu.Unlock()
}

func (r *connRing) snapshot(after uint64) (uint64, []ConnEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ConnEntry, 0, 128)
	for _, e := range r.buf {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return r.seq, out
}

// note кладёт запись в кольцо. Молчит, пока панель не попросила у ноды уровень
// debug: имя сайта и адрес устройства — это то, что человек сейчас смотрит, и
// в постоянно включённом журнале ему делать нечего.
func (p *Proxy) note(e ConnEntry) {
	p.mu.RLock()
	on := p.debug
	p.mu.RUnlock()
	if !on {
		return
	}
	p.connLog.add(e)
}

// LogHandler отдаёт последние соединения в том же виде, что и журнал запросов
// у dns-frontend: {"enabled":bool,"seq":N,"entries":[...]}. Выключенная отладка
// — это не ошибка, а состояние, и панель должна уметь сказать об этом словами.
func (p *Proxy) LogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p.mu.RLock()
		on := p.debug
		p.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if !on {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"enabled": false, "seq": 0, "entries": []ConnEntry{},
			})
			return
		}
		after, _ := strconv.ParseUint(req.URL.Query().Get("after"), 10, 64)
		seq, entries := p.connLog.snapshot(after)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true, "seq": seq, "entries": entries,
		})
	}
}

// clientIP — адрес устройства без порта. Порт эфемерный и в журнале только
// мешает глазу искать повторы с одного устройства.
func clientIP(c net.Conn) string {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	if c.RemoteAddr() == nil {
		return ""
	}
	return c.RemoteAddr().String()
}
