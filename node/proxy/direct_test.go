package proxy

import (
	"net"
	"testing"
	"time"
)

// Прямой выход обязан отказаться соединяться сам с собой. Иначе имя сервиса,
// разрешённое в адрес этой же ноды, закольцует прокси: он примет соединение,
// наберёт себя, снова примет — и так до конца сокетов.
func TestDialDirectRefusesLoop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.Close() }()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	// Обе стороны на петлевом адресе — ровно тот случай, что надо отбить.
	if c, err := dialDirect("127.0.0.1", port, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("соединение с самой собой должно быть отвергнуто")
	}
}

func TestDialDirectFailsFast(t *testing.T) {
	// Порт, на котором никто не слушает: ошибка, а не зависание.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	start := time.Now()
	if c, err := dialDirect("127.0.0.1", port, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("ожидалась ошибка соединения")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("набор висел %s вместо быстрого отказа", d)
	}
}

// Журнальная метка не должна разыменовывать пустую цель: при прямом выходе
// ноды-цели нет, а аргументы slog вычисляются независимо от уровня журнала.
func TestExitLabelHandlesNilTarget(t *testing.T) {
	if got := exitLabel(nil); got == "" {
		t.Fatal("метка пустая")
	}
	tg := &target{}
	tg.Name = "egress-us"
	if got := exitLabel(tg); got != "egress-us" {
		t.Fatalf("ожидалось имя ноды, получено %q", got)
	}
}
