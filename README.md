# Brainhub Proxy Service

Локальный HTTP-сервис, который:
- выполняет OAuth-логин через OpenAI/Auth0 flow
- сохраняет полученные токены в пользовательский профиль
- поднимает OpenAI-совместимый прокси для `POST /v1/responses`
- проксирует стриминг в ChatGPT Codex Responses API
- на Windows может работать как tray-приложение или как обычная Windows service

Сервис нужен как тонкий локальный мост между клиентами, которые умеют говорить в OpenAI Responses API, и upstream ChatGPT Codex endpoint.

## Что входит в проект

- `cmd/login`:
  CLI для OAuth-логина
- `cmd/proxy`:
  обычный консольный HTTP-сервис
- `cmd/proxytray`:
  Windows tray-обёртка вокруг прокси
- `cmd/updater`:
  отдельный бинарь для применения обновлений и rollback
- `internal/oauth`:
  PKCE login-flow, callback-сервер, обмен кода на токены, сохранение `auth.json`
- `internal/proxy`:
  OpenAI-совместимый HTTP handler
- `internal/proxyapp`:
  общий lifecycle запуска HTTP-сервера для CLI и tray
- `internal/remote`:
  upstream HTTP-клиент и нормализация payload
- `internal/authstore`:
  единое место, где определяется путь к auth-файлу
- `internal/buildinfo`:
  build metadata, вшиваемая через `-ldflags`
- `internal/update`:
  update manifest, сравнение версий, загрузка bundle и staging

## Как это работает

Полный поток выглядит так:

1. Вы запускаете `login`.
2. Приложение поднимает локальный callback-сервер на `http://localhost:1455/auth/callback`.
3. В браузере проходит OAuth-авторизация.
4. CLI получает `authorization_code`, обменивает его на токены и сохраняет их в `auth.json`.
5. Вы запускаете `proxy` или `proxytray`.
6. Прокси читает `tokens.access_token` и `tokens.account_id` из `auth.json`.
7. Локальный endpoint `POST /v1/responses` принимает OpenAI-совместимый запрос.
8. Сервис нормализует payload и перенаправляет его в `https://chatgpt.com/backend-api/codex/responses`.
9. SSE-ответ возвращается клиенту почти без изменений.

## Где хранятся данные

По умолчанию сервис использует пользовательскую папку `.brainhub`.

Linux/macOS:

```text
~/.brainhub/
```

Windows:

```text
%USERPROFILE%\.brainhub\
```

Основные файлы:

- `auth.json`:
  OAuth-токены и метаданные последнего обновления
- `tray.log`:
  лог tray-приложения на Windows
- `downloads/`:
  скачанные update bundles и plan-файлы updater'а
- `updates/`:
  распакованные staging-каталоги для применения обновлений
- `update.lock`:
  lock-файл, который не даёт запускать два обновления одновременно

Содержимое `auth.json`:

- `OPENAI_API_KEY`
- `tokens.id_token`
- `tokens.access_token`
- `tokens.refresh_token`
- `tokens.account_id`
- `last_refresh`

`proxy` использует только:

- `tokens.access_token`
- `tokens.account_id`

## Требования

- Go `1.25`
- доступ в интернет к `auth.openai.com` и `chatgpt.com`
- браузер для OAuth-логина
- свободный локальный порт `1455` для callback-сервера логина

Для Windows tray:

- Windows user session с доступом к системному трею

Для Windows service:

- запускать нужно `proxy.exe`, не `proxytray.exe`

## Быстрый старт

### 1. Логин

```bash
go run ./cmd/login login
```

Опции:

- `--no-browser`:
  не открывать браузер автоматически
- `--verbose`:
  подробный лог OAuth-процесса

После успешного логина токены сохраняются в:

```text
~/.brainhub/auth.json
```

На Windows:

```text
%USERPROFILE%\.brainhub\auth.json
```

Если callback не сработал автоматически, CLI умеет принять вручную вставленный redirect URL из браузера.

### 2. Запуск прокси

```bash
go run ./cmd/proxy --listen :8080
```

Параметры:

- `--listen`:
  адрес прослушивания, по умолчанию `:8080`
- `--auth-file`:
  путь к `auth.json`, по умолчанию `~/.brainhub/auth.json`
- `--upstream`:
  upstream base URL, по умолчанию `https://chatgpt.com`
- `--read-timeout`:
  timeout чтения HTTP-запроса, по умолчанию `30s`
- `--write-timeout`:
  timeout записи ответа, по умолчанию `0`
- `--service-name`:
  имя сервиса в structured logs, по умолчанию `brainhub-proxy`
- `--update-manifest-url`:
  URL release manifest для проверки обновлений
- `--update-channel`:
  канал обновлений, например `stable` или `beta`
- `--update-public-key`:
  base64-ed25519 public key для проверки подписи manifest
- `--auto-update`:
  режим `off`, `check` или `apply`
- `--update-check-interval`:
  интервал фоновой проверки обновлений
- `--windows-service-name`:
  имя Windows service для restart через `updater`

### 3. Проверка health endpoint

```bash
curl http://localhost:8080/healthz
```

Ожидаемый ответ:

```json
{
  "ok": true,
  "version": "v1.2.3",
  "commit": "abc123",
  "build_date": "2026-03-06T00:00:00Z",
  "channel": "stable",
  "service": "brainhub-proxy"
}
```

### 4. Пример запроса к `POST /v1/responses`

```bash
curl -N http://localhost:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": "Напиши hello world на Go",
    "stream": true
  }'
```

## API сервиса

Сервис поднимает три endpoint'а:

- `GET /healthz`
- `GET /versionz`
- `POST /v1/responses`

### `GET /healthz`

Назначение:
- liveness/readiness probe

Поведение:
- `200 OK` для `GET`
- `405` для остальных методов

Ответ:
- `ok`
- `version`
- `commit`
- `build_date`
- `channel`
- `service`

### `GET /versionz`

Назначение:
- получить build metadata без проверки readiness

Поведение:
- `200 OK` для `GET`
- `405` для остальных методов

### `POST /v1/responses`

Назначение:
- принять OpenAI-совместимый request
- преобразовать его к формату ChatGPT Codex
- отправить запрос в upstream
- вернуть SSE клиенту

Текущие ограничения:

- поддерживается только `stream=true`
- `model` обязателен
- `input` обязателен
- `reasoning`, если передан, должен быть объектом с `reasoning.effort`

Поддерживаемые поля входного запроса:

- `model`
- `instructions`
- `input`
- `tools`
- `tool_choice`
- `parallel_tool_calls`
- `reasoning`
- `stream`

`input` может быть:

- строкой
- объектом
- массивом

Если `input` передан строкой, сервис нормализует его к internal message format.

## Как сервис маппит запросы

Локальный API:

```text
POST /v1/responses
```

Upstream API:

```text
POST https://chatgpt.com/backend-api/codex/responses
```

В upstream-запрос добавляются:

- Bearer-токен из `tokens.access_token`
- account id из `tokens.account_id`
- session id
- нормализованный payload

Ответ читается как SSE-поток и пересылается клиенту построчно.

## Логирование

Консольный `proxy` использует `zap` в development-формате.

Логируются:

- запуск сервиса
- service name
- upstream request body
- upstream response status
- SSE-строки апстрима
- ошибки запроса и стрима

Windows `proxytray` пишет лог в файл:

```text
%USERPROFILE%\.brainhub\tray.log
```

## Сборка

### Локальная сборка

```bash
go build ./cmd/login
go build ./cmd/proxy
```

### Тесты

```bash
go test ./...
```

Интеграционные тесты:

- `internal/remote/chatgpt_codex_client_test.go`
- `internal/proxy/server_integration_test.go`

Они используют реальный upstream и запускаются только если доступен валидный:

```text
~/.brainhub/auth.json
```

### Сборка exe для Windows

```bash
mkdir -p build/windows
GOOS=windows GOARCH=amd64 go build -o build/windows/login.exe ./cmd/login
GOOS=windows GOARCH=amd64 go build -o build/windows/proxy.exe ./cmd/proxy
GOOS=windows GOARCH=amd64 go build -o build/windows/proxytray.exe ./cmd/proxytray
```

## Windows tray

Tray-бинарь:

```bash
go run ./cmd/proxytray
```

Что делает:

- запускает локальный proxy server
- показывает иконку в системном трее
- позволяет запустить login challenge прямо из tray
- позволяет скопировать login URL в буфер обмена
- позволяет открыть `.brainhub`
- позволяет открыть `tray.log`
- позволяет завершить сервис

Параметры:

- `--listen`
- `--auth-file`
- `--log-file`
- `--upstream`
- `--read-timeout`
- `--write-timeout`
- `--service-name`
- `--update-manifest-url`
- `--update-channel`
- `--update-public-key`
- `--auto-update`
- `--update-check-interval`

Рекомендуемый порядок запуска на Windows:

1. Собрать бинарь.
2. Выполнить `login.exe login`.
3. Проверить, что появился `%USERPROFILE%\.brainhub\auth.json`.
4. Запустить `proxytray.exe`.

## Установка как Windows service

Для фонового сервисного режима используйте только `proxy.exe`.

`proxytray.exe` не подходит для установки как service, потому что зависит от интерактивной пользовательской сессии и системного трея.

## Автообновление

Release manifest может быть подписан `ed25519`-подписью. Клиент проверяет:

- что версия в manifest новее текущей
- что bundle для текущей платформы есть в `platforms`
- что `sha256` скачанного архива совпадает
- что подпись manifest валидна, если задан `--update-public-key` или ключ вшит в build metadata

Во время установки создаётся `%USERPROFILE%\.brainhub\update.lock`, чтобы tray и service не начали обновление одновременно.

### Пример установки через `sc.exe`

```powershell
$ProxyExe = "C:\brainhub\proxy.exe"
$AuthFile = "$env:USERPROFILE\.brainhub\auth.json"

sc.exe create BrainhubProxy binPath= "\"$ProxyExe\" --listen 127.0.0.1:8080 --auth-file \"$AuthFile\" --service-name BrainhubProxy" start= auto
sc.exe description BrainhubProxy "Brainhub local OpenAI-compatible proxy"
sc.exe start BrainhubProxy
```

### Полезные команды управления service

```powershell
sc.exe query BrainhubProxy
sc.exe stop BrainhubProxy
sc.exe start BrainhubProxy
sc.exe delete BrainhubProxy
```

### Важные замечания по service account

- если service работает не от вашего пользователя, `%USERPROFILE%` будет другим
- поэтому для service лучше явно передавать `--auth-file`
- если нужен ваш пользовательский `auth.json`, сервис должен запускаться под тем же пользователем либо файл должен быть скопирован в профиль service account

## Типовые сценарии

### Локальная разработка

```bash
go run ./cmd/login login
go run ./cmd/proxy --listen :8080
```

### Фоновый запуск на Windows через tray

```powershell
.\proxytray.exe
```

### Фоновый запуск на Windows как service

```powershell
sc.exe start BrainhubProxy
```

## Troubleshooting

### `auth file path is required`

Причина:
- передан пустой `--auth-file`

Что проверить:
- не переопределяется ли флаг пустым значением

### `read auth file: ... no such file or directory`

Причина:
- логин ещё не выполнялся
- `auth.json` лежит не там, где его ищет сервис

Что делать:

1. Выполнить `login`.
2. Проверить наличие `~/.brainhub/auth.json`.
3. Если нужно, явно передать `--auth-file`.

### `auth file is missing tokens.access_token` или `tokens.account_id`

Причина:
- `auth.json` повреждён
- логин не завершился корректно

Что делать:

1. Выполнить логин повторно.
2. Пересоздать `auth.json`.

### `address already in use`

Причина:
- занят порт `1455` во время логина
- занят порт, указанный в `--listen`, во время старта прокси

Что делать:

- освободить порт
- либо выбрать другой адрес запуска прокси

### Tray не появляется

Проверить:

- запускается ли приложение именно на Windows
- есть ли интерактивная пользовательская сессия
- создаётся ли `%USERPROFILE%\.brainhub\tray.log`

### Service не стартует

Проверить:

- существует ли путь к `proxy.exe`
- существует ли путь к `auth.json`
- имеет ли service account права на чтение `auth.json`
- корректно ли сформирована строка `binPath`

## Ограничения

- сейчас поддерживается только стриминговый режим `stream=true`
- проект ориентирован на Codex/ChatGPT upstream, а не на полный OpenAI API surface
- tray-режим реализован только для Windows
- успешная работа зависит от актуальности upstream endpoint и текущего OAuth flow

## Основные файлы

- [cmd/login/main.go](./cmd/login/main.go)
- [cmd/proxy/main.go](./cmd/proxy/main.go)
- [cmd/proxytray/main_windows.go](./cmd/proxytray/main_windows.go)
- [internal/oauth/login.go](./internal/oauth/login.go)
- [internal/proxy/server.go](./internal/proxy/server.go)
- [internal/proxyapp/app.go](./internal/proxyapp/app.go)
- [internal/remote/chatgpt_codex_client.go](./internal/remote/chatgpt_codex_client.go)
- [internal/authstore/path.go](./internal/authstore/path.go)
