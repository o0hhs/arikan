# Sync с Mikan upstream/dev — состояние

Ветка: `sync/stage1` (от `upstream/dev` @ 25eed474, собирается и проходит go test).
Откат: ветка/тег `backup/pre-upstream-sync-2026-11-01` (= наш main на 27dbb2ec).

## Масштаб

- Наш main = 300 коммитов поверх базы 82ac5b32; upstream/dev = 430 коммитов поверх неё.
- В upstream удалены почти все наши пакеты: `internal/panel/subs` → `internal/panel/subsrc`
  + `internal/sub/*` (model/profile/routing/build/mihomo/singbox/xray/serve/apps);
  `internal/node/{cascadetunnel,disk,dpi,egress,paneladdr,reverse,site,subsrv,substore,turn}`;
  `internal/nodeapi/{cascade,subscriptions,tunnel}`; `internal/duplex`;
  `internal/panel/domain/outbounds.go`; `internal/hostchan/dpi.go` (hostchan как пакет жив).
- ~60 000 строк кода, удалённого upstream, вне `internal/sub/` (294 файла без тестов).
- Миграции: у upstream своя нумерация (0050–0063 в dev, уже переиспользует «наши» номера
  по-своему), у нас 0032–0083 с другим смыслом. MERGE через goose невозможен — только
  перенарезка наших миграций после их хвоста.

## Уже сделано (stage 1, черновик)

Решения по общим файлам взяты «как есть» от нас для минимума, который нужен билду —
**это ещё не собирается**, перенос «в лоб» не подходит:
- domain/inbounds.go, grants.go, users.go, nodecheck.go; nodesync/sync.go+manager.go;
  settings/settings.go; api/{api,inbounds,nodes,settings,users,catalog}.go;
  store/db/{models,nodes,domain}.sql.go; app/panel.go, app/maintain.go;
  billing/billing.go, events/*, sub/routing/routes.go, sub/singbox tests, sub/mihomo/*
  (форковые файлы: turn, youtube, xrayconfig, nodeserve, fromnodes, import, mtproto…).

Вывод: переносить надо не файлы, а **фичи**, поверх архитектуры `internal/sub`.

## План по фичам (по убыванию приоритета)

1. **Автовыбор** — upstream уже есть (XrayJSON для Happ+INCY, `internal/sub/xray`,
   `settings.XrayJSON`, `serve.Config.XrayJSON`). Наш `internal/panel/subs/xrayconfig.go`
   выкидывается, форк переключается на upstream-путь.
2. **YouTube без рекламы** — наш `Routes.YouTubeExit` + `YouTubeExitHost` (siblings в
   `internal/sub/model`/`routing` у upstream). Нужно решить, как INCY получает выход:
   у upstream Xray-конфиг тоже не несёт состояния соединений ноды.
3. **incубатор node-inbound API** — в upstream убран `internal/node/inboundapi.go` и
   миграция 0082_inbound_api; в dev идёт ветка `feat/panel-inbound-hosts-storage` (уже
   влита). Проверить, закрывает ли она наш сценарий (правка подключений без агента).
4. **DPI (nfqws) / reverse / site / turn / cascade / nodeserve / disk / egress /
   paneladdr / substore / subsrv** — портировать поверх нового Orкестратора
   (`internal/node/engine.go`), если keep.
5. **Панель-фичи**: checkhost, geoip, monitor, dashboard, referral, s3backup,
   salenotice, webproxy, pgdump, agents, automation, campaigns, cascade, discounts,
   partners, turn — переносить как пакеты (почти самостоятельны), затем в panel.go.
6. **Store/queries/models миграции** — перенарезать.
7. **web/ и site/**, i18n — после Go-слоя.
8. **CI: test-rest** — upstream поменял сплит; наш `.github/test-groups` (удалён в dev)
   надо подстроить под их `ci.yml`.

## Известные upstream-баги/заметки

- `TestExitDNSConfigParses` в node требует ипв6-таймаут 100 (наши правки DNS-таймаута
  не перенесены — в dev тот же 100; если хотим 50 — отдельная правка уже в новом файле).
- Turn-копии (WL) в dev всё так же вне auto-группы — наш фикс `turnCopies` можно
  переложить в `internal/sub/model`/`mihomo`.
