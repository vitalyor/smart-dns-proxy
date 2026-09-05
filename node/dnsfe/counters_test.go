package dnsfe

import (
	"testing"
	"time"
)

// Отказанный запрос не должен попадать в учёт: иначе чужой перебор токенов
// тратит квоту владельца. Проверяем и подрезание набора.
func TestCountersRetainDropsRevokedTokens(t *testing.T) {
	c := newCounters()
	now := time.Now()
	c.hit("aaa", now)
	c.hit("bbb", now)
	c.hit("", now) // без токена не атрибутируется
	if got := len(c.snapshot()); got != 2 {
		t.Fatalf("после двух токенов в учёте %d записей", got)
	}
	c.retain([]string{"AAA"}) // регистр не должен мешать
	snap := c.snapshot()
	if len(snap) != 1 || snap["aaa"].Queries != 1 {
		t.Fatalf("retain оставил %+v, ожидался только aaa", snap)
	}
}
