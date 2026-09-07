// Страна ноды: код ISO 3166-1 alpha-2. Флаг не картинка и не шрифт — две
// региональные буквы, которые система сама рисует флагом. Ничего не грузится.
export function flagOf(code: string): string {
  const c = code.trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(c)) return "";
  return String.fromCodePoint(...[...c].map((ch) => 0x1f1e6 + ch.charCodeAt(0) - 65));
}

// Список неполный намеренно: здесь то, где реально стоят точки выхода. Если
// понадобится страна вне списка — добавить строку сюда.
export const COUNTRIES: { code: string; name: string }[] = [
  { code: "RU", name: "Россия" },
  { code: "NL", name: "Нидерланды" },
  { code: "DE", name: "Германия" },
  { code: "LT", name: "Литва" },
  { code: "LV", name: "Латвия" },
  { code: "EE", name: "Эстония" },
  { code: "PL", name: "Польша" },
  { code: "FI", name: "Финляндия" },
  { code: "SE", name: "Швеция" },
  { code: "NO", name: "Норвегия" },
  { code: "DK", name: "Дания" },
  { code: "GB", name: "Великобритания" },
  { code: "IE", name: "Ирландия" },
  { code: "FR", name: "Франция" },
  { code: "ES", name: "Испания" },
  { code: "PT", name: "Португалия" },
  { code: "IT", name: "Италия" },
  { code: "CH", name: "Швейцария" },
  { code: "AT", name: "Австрия" },
  { code: "CZ", name: "Чехия" },
  { code: "RO", name: "Румыния" },
  { code: "BG", name: "Болгария" },
  { code: "MD", name: "Молдова" },
  { code: "TR", name: "Турция" },
  { code: "US", name: "США" },
  { code: "CA", name: "Канада" },
  { code: "BR", name: "Бразилия" },
  { code: "AE", name: "ОАЭ" },
  { code: "IL", name: "Израиль" },
  { code: "IN", name: "Индия" },
  { code: "SG", name: "Сингапур" },
  { code: "HK", name: "Гонконг" },
  { code: "JP", name: "Япония" },
  { code: "KR", name: "Южная Корея" },
  { code: "AU", name: "Австралия" },
  { code: "KZ", name: "Казахстан" },
  { code: "AM", name: "Армения" },
  { code: "GE", name: "Грузия" },
];

export function countryName(code: string): string {
  const c = code.trim().toUpperCase();
  return COUNTRIES.find((x) => x.code === c)?.name ?? c;
}
