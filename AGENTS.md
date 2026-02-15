# Пакеты и зоны ответственности

Репозиторий: `chatmock` (Go). HTTP-сервис-прокси с OpenAI-совместимым (`/v1/...`) и Ollama-совместимым (`/api/...`) API.

- `chatmock/cmd/chatmock`: точка входа в приложение, запуск HTTP-сервера (`main.go`).
- `chatmock/internal/app`: сборка приложения и инфраструктурная склейка: создание `Server`, регистрация роутов, чтение env и начальная конфигурация провайдеров, загрузка ChatGPT токенов из JSON-бандла.
- `chatmock/internal/api`: HTTP-обработчики роутов (`/v1/*`, `/api/*`), преобразование payload'ов между форматами, выбор поведения "мок по правилам" или проксирование в апстрим, формирование ответов и базовая утилита `writeJSON`.
- `chatmock/internal/remote`: описание провайдера (`Provider`), in-memory менеджер провайдеров (upsert/set/list/match), маршрутизация по `model_prefix`/`route_all`, HTTP-клиент проксирования в разные апстримы (OpenAI-compatible, ChatGPT `backend-api`, Ollama) и установка auth-заголовков.
- `chatmock/internal/chat`: общие типы структур для запросов/ответов (OpenAI-style chat completions, models list, responses API).
- `chatmock/internal/rules`: thread-safe хранилище правил моков (substring match `contains -> reply`) и CRUD для `/v1/rules`.

