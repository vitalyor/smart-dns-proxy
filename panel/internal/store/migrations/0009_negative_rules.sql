-- Отрицательные правила: исключение, которое нельзя вычесть удалением строки
-- («весь example.com через прокси, кроме private.example.com»), хранится как
-- запись вида not_* и доезжает до матчера вычитанием.
ALTER TABLE rule_entries DROP CONSTRAINT IF EXISTS rule_entries_kind_check;
ALTER TABLE rule_entries ADD CONSTRAINT rule_entries_kind_check
  CHECK (kind IN ('exact','suffix','regex','not_exact','not_suffix','not_regex'));
