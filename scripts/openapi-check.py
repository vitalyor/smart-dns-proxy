#!/usr/bin/env python3
"""Спецификация должна описывать каждый маршрут панели.

Документация, описывающая половину API, хуже отсутствующей: ей верят. Поэтому
проверка сравнивает docs/openapi.json с реальными маршрутами из routes.go и
падает, если появился неописанный путь или описан несуществующий.
"""
import json
import re
import sys

SPEC = "docs/openapi.json"
ROUTES = "panel/internal/api/routes.go"

# Группы регистрируются в цикле по ingress/egress, буквального шаблона в коде нет.
DYNAMIC = {
    "/{role}-groups": ["/ingress-groups", "/egress-groups"],
    "/{role}-groups/{id}": ["/ingress-groups/{id}", "/egress-groups/{id}"],
    "/{role}-groups/{id}/members": ["/ingress-groups/{id}/members", "/egress-groups/{id}/members"],
    "/{role}-groups/{id}/members/{node_id}": [
        "/ingress-groups/{id}/members/{node_id}",
        "/egress-groups/{id}/members/{node_id}",
    ],
}


def norm(p: str) -> str:
    return re.sub(r"\{[^}]+\}", "{x}", p)


def main() -> int:
    spec = json.load(open(SPEC))
    src = open(ROUTES).read()

    routes = set()
    for path in re.findall(r'HandleFunc\("(?:GET|POST|PUT|PATCH|DELETE) ([^"]+)"', src):
        routes.add(path)
    # маршруты групп собираются из шаблона base := "/" + k.role + "-groups"
    if 'k.role + "-groups"' in src or '"/" + k.role' in src:
        for variants in DYNAMIC.values():
            routes.update(variants)

    spec_norm = {norm(p) for p in spec["paths"]}
    route_norm = {norm(p) for p in routes}

    missing = sorted(p for p in routes if norm(p) not in spec_norm)
    extra = sorted(p for p in spec["paths"] if norm(p) not in route_norm)

    for p in missing:
        print(f"нет в openapi.json: {p}", file=sys.stderr)
    for p in extra:
        print(f"описан, но маршрута нет: {p}", file=sys.stderr)
    if missing or extra:
        print(f"\nне описано {len(missing)}, лишних {len(extra)}", file=sys.stderr)
        return 1
    print(f"openapi.json: ok, {len(spec['paths'])} путей покрывают все маршруты")
    return 0


if __name__ == "__main__":
    sys.exit(main())
