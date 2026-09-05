/* Тему проставляем до первой отрисовки, иначе выбранный тёмный интерфейс на
   миг вспыхивает светлым. Отдельный файл, а не inline: CSP панели разрешает
   только script-src 'self'. */
try {
  var t = localStorage.getItem("sdns-theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch (e) { /* приватный режим — остаётся системная тема */ }
