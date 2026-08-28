# SmartDNS — сборка, тесты и локальный стенд.
SHELL := /bin/bash
VERSION ?= 2.0.0
GOFLAGS ?= -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION)
LAB := deploy/examples/lab/docker-compose.yml

.PHONY: help
help: ## Показать список целей
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: web ## Собрать все бинарники в ./bin
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/ \
		./panel/cmd/panel-api ./agent/cmd/node-agent \
		./node/cmd/dns-frontend ./node/cmd/sni-proxy ./node/cmd/egress-relay
	@echo "→ bin/"

.PHONY: web
web: ## Собрать веб-панель
	cd panel/web && npm ci --no-audit --no-fund 2>/dev/null || (cd panel/web && npm install --no-audit --no-fund)
	cd panel/web && npm run build

.PHONY: test
test: ## Юнит- и интеграционные тесты Go
	go test ./...

.PHONY: test-race
test-race: ## Тесты с детектором гонок
	go test -race ./...

.PHONY: lint
lint: ## go vet + проверка форматирования
	go vet ./...
	@out=$$(gofmt -l . | grep -v node_modules || true); \
	 if [ -n "$$out" ]; then echo "Не отформатировано:"; echo "$$out"; exit 1; fi

.PHONY: e2e
e2e: ## Полный стенд в Docker со сквозными проверками
	./scripts/lab-e2e.sh

.PHONY: lab-up
lab-up: ## Поднять стенд и оставить работать
	./scripts/lab-e2e.sh --keep

.PHONY: lab-down
lab-down: ## Остановить стенд и удалить тома
	docker compose -f $(LAB) down -v

.PHONY: lab-logs
lab-logs: ## Журналы стенда
	docker compose -f $(LAB) logs -f

.PHONY: images
images: ## Собрать production-образы
	docker compose build
	docker compose -f node/deploy/ingress/docker-compose.yml build
	docker compose -f node/deploy/egress/docker-compose.yml build

.PHONY: openapi
openapi: ## Проверить, что спецификация OpenAPI разбирается
	@python3 -c "import json,sys; json.load(open('docs/openapi.json')); print('openapi.json: ok')"

.PHONY: check
check: lint test openapi ## Всё, что должно проходить перед коммитом

.PHONY: clean
clean: ## Удалить артефакты сборки
	rm -rf bin panel/web/dist panel/web/node_modules panel/web/tsconfig.tsbuildinfo
