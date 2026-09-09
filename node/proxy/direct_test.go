package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"smartdns/shared/model"
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
	if c, err := dialDirect(net.DefaultResolver, "127.0.0.1", port, time.Second); err == nil {
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
	if c, err := dialDirect(net.DefaultResolver, "127.0.0.1", port, time.Second); err == nil {
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

// Пустой список нод выхода обязан быть ошибкой. Раньше dial возвращал разом
// nil-соединение и nil-ошибку, вызывающий считал это удачей и писал в пустоту —
// прокси падал, а вместе с ним ложился весь вход, не только этот сервис.
func TestDialWithoutTargetsErrors(t *testing.T) {
	po := newPool(model.EgressPolicy{}, nil, nil)
	c, tg, err := po.dial(nil, "example.com", 443, time.Second)
	if err == nil {
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("ожидалась ошибка, получен успех без соединения")
	}
	if c != nil || tg != nil {
		t.Fatal("при ошибке ни соединения, ни цели быть не должно")
	}
}

// Имена нод выхода обязаны разрешаться нашим резолвером, а не системным.
// Системный на входе один — служебный резолвер хостера, и он же оказывался
// единой точкой отказа: каждое новое соединение и каждая проверка здоровья
// спрашивали у него адрес ноды заново. Он захлёбывался, вход переставал
// находить выход, и разом умирали все проксируемые сервисы.
func TestPoolUsesConfiguredResolver(t *testing.T) {
	dead := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("резолвер-заглушка")
	}}
	po := newPool(model.EgressPolicy{Targets: []model.EgressTarget{
		{NodeID: "n1", Name: "egress-test", Endpoint: "relay.invalid:8443", SNI: "relay.invalid"},
	}}, nil, dead)

	_, _, err := po.dial(&tls.Config{}, "example.com", 443, 2*time.Second)
	if err == nil {
		t.Fatal("ожидалась ошибка: подставной резолвер не может разрешить имя")
	}
	if !strings.Contains(err.Error(), "резолвер-заглушка") {
		t.Fatalf("пул не воспользовался переданным резолвером, ошибка: %v", err)
	}
}
