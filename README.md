# traffic-limiter

Автономный Go-сервис, который поверх панели **Remnawave** даёт две вещи, недоступные из коробки:

1. **Тарификация только «белых» нод** — при исчерпании белого лимита отрубаются *только* белые ноды, а «обычные» (basic) продолжают работать.
2. **Отдельный лимит на basic-ноды** — считается самим сервисом через периодический опрос статистики панели.

Сервис **полностью самостоятелен**: работает без бота bedolaga (или любого другого). Код бота **не модифицируется** — это сознательное решение, чтобы обновления бота ничего не затирали. При желании сервис может опционально перенаправлять обработанные события в готовый эндпоинт бота `/remnawave-webhook` (только HTTP-вызов, без правок кода).

---

## Почему так

Лимит трафика в Remnawave задаётся на уровне пользователя (`trafficLimitBytes`) и проверяется по всему аккаунту. `consumptionMultiplier` (коэффициент) у ноды управляет лишь скоростью начисления `used`, а не зоной блокировки. Поэтому схема «у обычных 0, у белых 1» честно считает трафик, но при исчерпании глушит подписку полностью. Этот сервис решает задачу оркестрацией squad'ов через API.

---

## Архитектура

```
                ┌─────────────────── Remnawave Panel ────────────────────┐
                │  user.limited          → webhook (HMAC-SHA256)          │
                │  user.traffic_reset    → webhook                        │
                │  /api/bandwidth-stats/nodes/{uuid}/users → poller       │
                └─────────────────────────────┬───────────────────────────┘
                                              │ POST /webhook
                                              ▼
                          ┌──────────────────────────────────┐
                          │   traffic-limiter (Go)           │
                          │                                  │
                          │  • verify HMAC-SHA256 sig        │
                          │  • engine: cut/restore squads    │
                          │  • poller: count basic traffic   │
                          │  • reconciler: lost-webhook fix  │
                          │  • SQLite state                  │
                          └────────────┬─────────────────────┘
                                       │ PATCH /api/users
                                       │ (activeInternalSquads, trafficLimitBytes)
                                       ▼
                                   панель применяет

            Optional (BOT_WEBHOOK_URL set):
                          ┌──────────────────────────────────┐
                          │   botrelay (Go)                  │
                          │   • forwards processed events    │
                          │   • user.limited → user.modified │
                          │     (status=ACTIVE)              │
                          │   • HMAC-signs the request       │
                          └────────────┬─────────────────────┘
                                       │ POST /remnawave-webhook
                                       ▼
                          bedolaga bot (untouched)
```

| Squad | Inbounds | consumptionMultiplier | Кто считает лимит |
|---|---|---|---|
| `basic` | обычные ноды | `0` | этот сервис (поллит stats) |
| `whitelist` | белые ноды | `1` | сама панель (`trafficLimitBytes`) |

Пользователь состоит в **обоих** squad'ах по умолчанию.

### Поведение при лимитах

- **Whitelist исчерпан:** `user.limited` → сервис переводит юзера в **грейс** (окно по времени и/или over-limit), затем убирает `whitelist` squad и возвращает `ACTIVE`. Дополнительно временно ставит огромный `trafficLimitBytes` + `NO_RESET`, чтобы панель не упала обратно в `LIMITED`.
- **Basic исчерпан:** poller видит `basic_used ≥ basic_limit` → убирает `basic` squad.
- **Сброс:** по `user.traffic_reset`/`user.data_used_reset` или внешнему `POST /admin/repay/{uuid}` — оба squad'а возвращаются, счётчики обнуляются.
- **Докупка трафика (кнопка бота):** бот шлёт `PATCH trafficLimitBytes` → панель фаерит `user.modified` (не `traffic_reset`!). Сервис ловит его и возвращает whitelist squad, если юзер действительно получил дополнительный лимит (проверка: новый `trafficLimitBytes` больше сохранённого оригинала ИЛИ `usedTrafficBytes` упал ниже оригинала). Это важно: без этого хендлера кнопка «📊 докупить трафик» в боте **не возвращала бы белые ноды** — юзер платил бы впустую. Наш же Plan-B override (~1 EiB лимит) намеренно игнорируется, чтобы не было ложных разблокировок.

### Как работает докупка трафика в боте (end-to-end)

| Шаг | Кто | Что делает |
|---|---|---|
| 1 | Юзер | Жмёт «📊 докупить трафик» в боте. Бот списывает деньги. |
| 2 | Бот | `PATCH /api/users` с новым `trafficLimitBytes` (повышенным). |
| 3 | Панель | Применяет, шлёт webhook `user.modified` с обновлёнными `trafficLimitBytes`/`usedTrafficBytes`. |
| 4 | **traffic-limiter** | `onUserModified` видит: юзер в `blocked`, новый лимит > оригинала → возвращает `whitelist` squad, восстанавливает нормальный лимит и стратегию (перетирая Plan-B). |
| 5 | Юзер | Белые ноды снова работают, при следующем окне бот (если включён relay) увидит `user.modified` и обновит счётчики. |

Альтернатива — кнопка «🔄 сбросить трафик»: бот шлёт `POST /actions/reset-traffic`, панель обнуляет `usedTrafficBytes` и фаерит `user.traffic_reset`, сервис возвращает whitelist по основному пути `onUserReset`. Обе ветки работают.

---

## Развёртывание: автономный режим (без бота)

Рекомендуемый старт. Сервис не зависит от бота вообще.

1. **Настройте панель** (см. чек-лист ниже).
2. В панели **Settings → Webhooks** направьте webhook на этот сервис: `https://<traffic-limiter-host>/webhook`.
3. Запустите:

```bash
cp .env.example .env
# заполните обязательные переменные (без BOT_WEBHOOK_URL)

# Создайте папку для базы данных и выдайте права (чтобы не было проблем с permission denied)
mkdir -p data
chown 65532:65532 data

docker compose -f deployments/docker-compose.yml up -d --build
```

> **Совет по безопасности (общение внутри локальной сети Docker)**:
> Если панель развернута на этом же сервере, лучше настроить их общение по локальной сети, чтобы не гонять трафик через внешний IP.
> 1. Узнайте точное имя сети вашей панели с помощью команды: `docker network ls` (обычно это `remnawave_default` или `remnawave-main_default`).
> 2. Откройте `deployments/docker-compose.yml`, в самом низу удалите строку `external: true` и раскомментируйте `name: remnawave_default` (подставив имя вашей сети).
> 3. Теперь в `.env` вы можете указать `REMNAWAVE_PANEL_URL=http://remnawave:3000` (без `/api` на конце).

Проверка: `curl http://localhost:8088/healthz` → `ok`.

---

## Развёртывание: с relay в бота bedolaga (опционально)

Если хотите, чтобы бот продолжал слать юзеру уведомления и вести свою статистику трафика:

1. В панели **тот же webhook URL** (`/webhook` этого сервиса). Панель шлёт webhook **только сюда** — бот свой отдельный webhook-приёмник в этом режиме не используется (`REMNAWAVE_WEBHOOK_ENABLED=false` у бота, или просто панель туда не шлёт).
2. В `.env` traffic-limiter:
   ```
   BOT_WEBHOOK_URL=https://<bot-host>/remnawave-webhook
   BOT_WEBHOOK_SECRET=<тот же секрет, что REMNAWAVE_WEBHOOK_SECRET у бота>
   ```
3. Сервис после обработки каждого события форвардит его в бота, подписывая HMAC-SHA256 заголовком `X-Remnawave-Signature` (ровно то, что проверяет `app/webserver/remnawave_webhook.py`).

**Трансляция событий:** `user.limited` отправляется в бот как `user.modified` со `status=ACTIVE`, чтобы бот не вешал подписке статус «исчерпан» (т.к. на самом деле basic ещё жив). Остальные события проходят as-is.

> Это **единственный** способ взаимодействия с ботом. Код бота не трогается — при его обновлении ничего не отвалится.

---

## Чек-лист настройки панели Remnawave

> После смены env-переменных панели нужно **пересоздать** контейнер (`docker compose up -d --force-recreate`).

### 1. Ноды
- Обычные: `consumptionMultiplier = 0`.
- Белые: `consumptionMultiplier = 1`.

### 2. Config Profile → Inbounds
Развяжите inbounds обычных и белых нод (разные `tag`/хосты), чтобы squad'ы могли адресовать именно свои ноды.

### 3. Internal Squads
- `basic` → inbounds обычных нод.
- `whitelist` → inbounds белых нод.

Запишите UUID'ы в `.env` (`BASIC_SQUAD_UUID`, `WHITELIST_SQUAD_UUID`).

### 4. Пользователи
Назначайте каждого пользователя в **оба** squad'а. `trafficLimitBytes` = величина **белого** лимита; `trafficLimitStrategy` = по вашему биллинговому циклу.

### 5. Webhook (env панели)
```
WEBHOOK_ENABLED=true
WEBHOOK_URL=https://<traffic-limiter-host>/webhook
WEBHOOK_SECRET=<значение из WEBHOOK_SECRET_VALUE сервиса>   # ≥ 32 символа
```
Подпись: HMAC-SHA256 от тела, заголовок `X-Remnawave-Signature` (hex).

### 6. API-токен
Settings → API Tokens → токен с правами на `Users`. Положить в `REMNAWAVE_API_TOKEN`.

---

## API этого сервиса

| Метод | Путь | Auth | Назначение |
|---|---|---|---|
| `POST` | `/webhook` | HMAC-SHA256 | Приём вебхуков от панели. |
| `GET` | `/healthz` | — | Liveness. |
| `GET` | `/api/state/{userUuid}` | Bearer (если `STATE_API_TOKEN`) | Снимок состояния: `basic.used/limit`, `whitelist.state/grace`, текущие значения в панели. |
| `POST` | `/admin/repay/{userUuid}` | Bearer `ADMIN_TOKEN` | Внешний «оплачено»: сброс счётчиков и возврат обоих squad'ов. |

Пример repay:
```bash
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://traffic-limiter.local/admin/repay/USER-UUID
```

Пример чтения состояния:
```bash
curl -H "Authorization: Bearer $STATE_API_TOKEN" \
  https://traffic-limiter.local/api/state/USER-UUID | jq
```

Ответ `/api/state/{uuid}`:
```json
{
  "user_uuid": "...",
  "basic":  { "used_bytes": 2147483648, "limit_bytes": 21474836480, "state": "active" },
  "whitelist": { "state": "blocked", "original_limit": {...}, "last_limited_at": {...} },
  "panel": {
    "status": "ACTIVE",
    "active_internal_squads": ["basic-uuid"],
    "traffic_limit_bytes": 1125899906842624,
    "used_bytes": 0,
    "strategy": "NO_RESET"
  }
}
```

---

## Переменные окружения

См. `.env.example`. Обязательные: `REMNAWAVE_PANEL_URL`, `REMNAWAVE_API_TOKEN`, `WEBHOOK_SECRET_VALUE`, `BASIC_SQUAD_UUID`, `WHITELIST_SQUAD_UUID`, `BASIC_NODE_UUIDS`.

Ключевые опциональные: `BOT_WEBHOOK_URL`/`BOT_WEBHOOK_SECRET` (relay в бота), `WHITELIST_GRACE_*`, `BASIC_POLL_INTERVAL_SEC`, `BASIC_DEFAULT_LIMIT_GB`, `ADMIN_TOKEN`, `STATE_API_TOKEN`.

Подписочный прокси: `SUBPROXY_ENABLED` (включить прокси `/sub/`), `WL_TITLE_ACTIVE`/`WL_TITLE_BLOCKED`/`WL_TITLE_EXPIRED` (заголовки для трёх веток), `FAILOVER_CONFIG` (rescue-сервер для истёкших по дате), `SUBPROXY_CACHE_TTL_SEC`.

---

## Тест-план

1. **Whitelist + грейс.** Юзер `trafficLimitBytes = 100 МБ`, `WHITELIST_GRACE_OVERLIMIT_MB=50`. Нагнать 100 МБ через белую ноду → `grace`. Нагнать ещё 50 (или подождать окно) → `whitelist` squad убран, белая не коннектится, basic работает, `status=ACTIVE`.
2. **Reset.** Дождаться `trafficLimitStrategy` или дёрнуть `/admin/repay/{uuid}` → счётчики обнулены, `whitelist` возвращён, белая снова работает.
3. **Basic лимит.** Нагнать трафик через basic-ноду до `basic_limit` → `basic` squad убран, whitelist (если активен) продолжает работать.
4. **Гонки.** Одновременно `limited` + `reset` → состояние консистентно (per-user mutex в engine).
5. **Потеря вебхука.** Сервис видит юзера `LIMITED`, хотя локально `active` → reconciler за `RECONCILE_INTERVAL_SEC` отрабатывает `user.limited`-флоу.
6. **Relay в бота.** При `BOT_WEBHOOK_URL` заданном: `user.limited` доходит до бота как `user.modified` `status=ACTIVE`; бот НЕ ставит подписке `LIMITED`. Проверить через логи бота.

---

## Структура проекта

```
cmd/orchestrator/        HTTP-сервер + фоновые воркеры + state handler
internal/
  config/                env-конфиг
  state/                 SQLite + миграции
  remnawave/             тонкий HTTP-клиент (activeInternalSquads, /actions/*, bandwidth-stats)
  webhook/               приём + HMAC-SHA256 проверка подписи
  engine/                decision logic, per-user lock, handlers, bot-relay glue
  botrelay/              опциональный форвард событий в бот (HMAC-signed)
  poller/                cron-опрос статистики basic-нод
  subproxy/              опциональный прокси подписок с подменой profile-title
deployments/             docker-compose
Dockerfile               multi-stage, CGO_ENABLED=0
```

---

## Подписочный прокси: надпись в шапке Happ / INCY + failover для истёкших

Все современные клиенты (Happ, INCY, v2rayNG, Karing) читают из подписки заголовок `profile-title` и показывают его в шапке приложения. Сервис умеет **подменять этот заголовок per-user** в зависимости от состояния подписки, а истёкшим по дате юзерам — выдавать **один rescue-сервер** (failover), чтобы они могли зайти в личный кабинет и продлить.

Три ветки поведения:

- подписка активна → заголовок панели **проходит как есть** (ваш бренд), тело отдаёт панель
- whitelist исчерпан (трафик кончился, подписка ещё действует) → `⚠️ Whitelist exhausted · basic nodes work`, тело отдаёт панель (basic-ноды работают)
- **подписка истекла по дате** (`expire` из `Subscription-Userinfo` в прошлом) → заголовок `FAILOVER_TITLE` (по умолчанию вашего бренда), тело = **только rescue-сервер** (`FAILOVER_CONFIG`)

> Ключевое отличие failover от снятия whitelist: при исчерпании **трафика** у юзера остаются рабочие basic-ноды и подписка числится активной до оплаченной даты — rescue-сервер тут **не** выдаётся. Rescue выдаётся **только** когда подписка реально истекла по дате.

**Как определяется истечение:** по полю `expire=` в заголовке `Subscription-Userinfo`, который панель сама отдаёт вместе с телом подписки (не отдельным запросом). `expire=0` трактуется как «без срока» (бессрочно) → не истёк. Этот способ выбран потому, что отдельный эндпоинт `/api/sub/{short}/info` на многих версиях панели медленный или вовсе зависает для истёкших юзеров, а заголовок `Subscription-Userinfo` приходит всегда и мгновенно.

Тексты задаются через env `WL_TITLE_ACTIVE` / `WL_TITLE_BLOCKED` / `WL_TITLE_EXPIRED`, rescue-сервер — через `FAILOVER_CONFIG` (emoji и кириллица поддерживаются — сервис URL-кодирует значение так, как ожидают клиенты).

### Как это работает

```
Happ/INCY ──GET──► /sub/{shortUuid}[/...]   (на traffic-limiter, не на панель)
                      │
                      ├─ проксирует запрос на панель /api/sub/{short}[...]
                      ├─ читает Subscription-Userinfo ответа:
                      │      expire в прошлом + задан FAILOVER_CONFIG →
                      │         отдаёт только rescue-сервер + title «истекла»
                      └─ иначе резолвит shortUuid → userUuid, читает wl_state,
                         и overlay'ит status title только когда whitelist исчерпан
                         (для активных заголовок панели проходит как есть)
```

> ⚠️ **Важно про ограничения клиентов.** Произвольный большой текст в шапке показать нельзя: `profile-title` — это **одна строка**. Счётчики `used/total` клиенты берут из стандартного `subscription-userinfo` (его панель отдаёт, мы не трогаем). Длинные «анонсы» поддерживают только Clash-семейство (через `Announce` в теле), но не Happ/INCY.

### Включение

1. В `.env`:
   ```
   SUBPROXY_ENABLED=true
   WL_TITLE_ACTIVE=🟢 VPN · whitelist active
   WL_TITLE_BLOCKED=⚠️ Whitelist exhausted · basic nodes work
   WL_TITLE_EXPIRED=
   FAILOVER_CONFIG=vless://...@rescue.example.com:443?...#%F0%9F%87%B7%F0%9F%87%BA%20TELEGRAM
   SUBPROXY_CACHE_TTL_SEC=300
   ```
2. У клиентов (через бот/миниапп) URL подписки меняется на
   `https://traffic-limiter.example.com/sub/{shortUuid}`
   (если клиенты стучат на корень домена без `/sub/`, см. пример rewrite в инструкции ниже).
3. Reverse-proxy перед traffic-limiter должен терминировать HTTPS на том же хосте, где отдаётся `/sub/`.

### Производительность

Каждый pull подписки → 1 запрос к панели (`/api/sub/{short}/info`) + 1 запрос на само тело (`/api/sub/{short}`), плюс статус юзера грузится через `/api/users/{uuid}` и кэшируется на `SUBPROXY_CACHE_TTL_SEC` (по умолчанию 5 минут). Чтобы не бить панель, короткий UUID → UUID пользователя и статус кэшируются отдельно. Холодный старт — первые минуты после рестарта — каждый pull стоит +1 запроса к панели; клиенты это не замечают (тело отдаётся в любом случае).

### Тест-план для прокси

1. `SUBPROXY_ENABLED=true`, юзер с `status=ACTIVE`, `wl_state=active`. `curl -i https://tl/sub/{short}` → `Profile-Title: 🟢%20VPN%20...`.
2. Юзер исчерпал whitelist, сервис перевёл его в `blocked`. Тот же curl → `Profile-Title: ⚠️%20Whitelist%20exhausted...`, тело содержит basic-ноды.
3. Юзер с истёкшей по дате подпиской (`status=EXPIRED`). Тот же curl → `Profile-Title: 🔴%20...`, тело содержит **только** rescue-сервер из `FAILOVER_CONFIG`.
4. В самом приложении Happ/INCY после обновления подписки — в шапке видно соответствующий текст.
5. Несуществующий shortUuid → панель вернёт 404, прокси проксирует его как есть + дефолтный title (active).

---

## Известные ограничения / TODO

- Формат ответа `/api/bandwidth-stats/nodes/{uuid}/users` сверен с актуальным API через код bedolaga-бота (`get_bandwidth_stats_node_users`). Если панель вернёт неизвестную форму — `parseNodeUsage` залогирует снипет; добавьте разбор в `internal/remnawave/usage.go`.
- Базовый лимит per-user задаётся через `BASIC_DEFAULT_LIMIT_GB` (один на всех). Per-user значения можно выставить через `engine.SetBasicLimit` программно; HTTP-эндпоинт для этого не сделан — добавьте при интеграции с биллингом.
- Relay делает одну попытку с одним ретраем; для production-нагрузки стоит добавить очередь (outbox) при больших объёмах.
