# ChatMock (Go)

HTTP-сервис-прокси с OpenAI-совместимым (`/v1/...`) и Ollama-совместимым (`/api/...`) API.

## Поддерживаемые роуты
### OpenAI-compatible
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/responses`
- `GET /v1/models`

### Ollama-compatible
- `POST /api/chat`
- `GET /api/tags`
- `POST /api/show`
- `GET /api/version`

### Service
- `GET /healthz`
- `GET/PUT /v1/providers`
- `GET/PUT /v1/rules`

## Запуск
```bash
go run ./cmd/chatmock
```

## Быстрый старт: Ollama + Codex + ChatGPT Account Proxy
```bash
CHATMOCK_OLLAMA_BASE_URL=http://localhost:11434 \
CHATMOCK_OLLAMA_MODEL_PREFIX=ollama/ \
CHATMOCK_CODEX_BASE_URL=https://api.openai.com \
CHATMOCK_CODEX_API_KEY=sk-... \
CHATMOCK_CODEX_MODEL_PREFIX=codex/ \
CHATMOCK_CHATGPT_BASE_URL=https://chatgpt.com \
CHATMOCK_CHATGPT_ACCESS_TOKEN=<access_token> \
CHATMOCK_CHATGPT_ACCOUNT_ID=<chatgpt_account_id> \
CHATMOCK_CHATGPT_MODEL_PREFIX=chatgpt/ \
go run ./cmd/chatmock
```

`chatgpt/*` модели будут проксированы в `POST /backend-api/codex/responses` с заголовками:
- `Authorization: Bearer <access_token>`
- `chatgpt-account-id: <account_id>`

> Сейчас реализован прокси-слой для уже полученных токенов. OAuth login-flow из Python-версии (`chatmock.py login`) ещё не портирован.
