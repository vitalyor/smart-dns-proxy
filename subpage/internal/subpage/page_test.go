package subpage

import (
	"regexp"
	"strings"
	"testing"
)

// Две вещи ломают нажатия так, что этого не видно ни в одном Go-тесте, а на
// телефоне выглядит как «кнопка иногда не работает». Оба раза виноват
// невидимый, но осязаемый слой поверх страницы, поэтому проверяем прямо здесь.
func TestOverlayDoesNotSwallowTaps(t *testing.T) {
	page := string(pageHTML)
	// Класс .sheet задаёт display:flex и перебивает браузерное [hidden].
	if !strings.Contains(page, "[hidden]{display:none !important}") {
		t.Error("скрытые элементы обязаны исчезать: без этого лист остаётся в потоке")
	}
	// Закрытый лист живёт ещё ~260 мс ради анимации ухода.
	for _, sel := range []string{".scrim{", ".sheet{"} {
		i := strings.Index(page, sel)
		if i < 0 {
			t.Fatalf("правило %s пропало", sel)
		}
		if !strings.Contains(page[i:i+600], "pointer-events:none") {
			t.Errorf("%s должен отключать события, пока не показан", sel)
		}
	}
}

// Имя устройства придумывает человек и оно попадает в атрибуты разметки.
func TestEscapeCoversAttributes(t *testing.T) {
	page := string(pageHTML)
	i := strings.Index(page, "function esc(s)")
	if i < 0 {
		t.Fatal("esc не найдена")
	}
	body := page[i : i+400]
	for _, want := range []string{`&quot;`, `&#39;`} {
		if !strings.Contains(body, want) {
			t.Errorf("esc не экранирует %s — кавычка в имени рвёт атрибуты кнопки", want)
		}
	}
}

// Кнопка, которую выключили, неотличима от сломанной: причину показываем текстом.
func TestPrimaryButtonIsNeverDisabled(t *testing.T) {
	page := string(pageHTML)
	if regexp.MustCompile(`\$\("cta"\)\.disabled`).MatchString(page) {
		t.Error("главную кнопку не гасим — она должна объяснять, почему сейчас нельзя")
	}
}
