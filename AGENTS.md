# Пакеты и зоны ответственности

- `internal/buildinfo`: build metadata для логов, release и update-check.
- `internal/update`: проверка manifest, сравнение версий, download/staging update bundle и apply-plan.
- `cmd/updater`: отдельный процесс для безопасной замены бинарников и rollback.

## Поддержка
- При добавлении нового Go-пакета добавлять краткую запись в "Пакеты и зоны ответственности" выше.

## Правила процесса
- После каждого таска запускать:
  - `goimports -w .`
  - `go vet ./...`
  - `golangci-lint run --config .golangci-lint.yaml ./... --timeout 1m`
  - `go test -short ./...`
- Если запуск невозможен, указать причину в ответе.
