-- Ноды выбираются прямо в сервисе, групп больше нет.
--
-- Группа была именованным списком нод с режимом раздачи. Режим «С весами»
-- перетасовывал ноды на каждое соединение, поэтому один клиент уходил к сайту
-- через несколько стран сразу — для сервисов, где важен аккаунт, это и есть
-- способ его потерять. Остаётся один порядок: первая живая по приоритету.
--
-- Роль ноды не дублируем: она уже есть в nodes.role. ON DELETE RESTRICT у
-- node_id намеренно: удаление ноды, которую использует сервис, должно
-- останавливаться с ошибкой, а не тихо оставлять сервис без выхода.
CREATE TABLE service_nodes (
  service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  node_id    uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
  priority   int  NOT NULL DEFAULT 1,
  PRIMARY KEY (service_id, node_id)
);
CREATE INDEX ON service_nodes (node_id);

-- Перенос: состав каждой группы становится списком нод сервиса. Нода не бывает
-- одновременно входной и выходной, поэтому две выборки не пересекаются.
INSERT INTO service_nodes (service_id, node_id, priority)
SELECT s.id, m.node_id, m.priority
  FROM services s JOIN ingress_group_members m ON m.group_id = s.ingress_group_id
 WHERE m.enabled
UNION ALL
SELECT s.id, m.node_id, m.priority
  FROM services s JOIN egress_group_members m ON m.group_id = s.egress_group_id
 WHERE m.enabled;

ALTER TABLE services DROP COLUMN ingress_group_id;
ALTER TABLE services DROP COLUMN egress_group_id;

DROP TABLE ingress_group_members;
DROP TABLE egress_group_members;
DROP TABLE ingress_groups;
DROP TABLE egress_groups;
