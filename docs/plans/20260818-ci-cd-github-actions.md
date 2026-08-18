# CI/CD для horizont-epub

## Overview

Добавить единые локальные команды и GitHub Actions для сборки, тестов, покрытия не ниже 80%, golangci-lint, govulncheck, Dependabot security updates и автоматического patch-релиза каждого PR, попавшего в `master`.

Релизный workflow создаёт уникальный тег серии `v0.1.x`, формирует описание из данных merged PR и публикует кроссплатформенные архивы через GoReleaser.

## Context

- Текущий модуль: `horizont-epub`, Go 1.25+, один `main` package.
- Текущие проверки: `go test ./...`, `go test -race ./...`, `go vet ./...`.
- Текущее покрытие: 83,1%, поэтому минимальный порог 80% не требует искусственных тестов.
- CI/CD, Makefile, golangci-lint, Dependabot и GoReleaser пока отсутствуют.
- Files involved: `Makefile`, `.golangci.yml`, `.goreleaser.yml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/dependabot.yml`, `.gitignore`, `README.md`, `CLAUDE.md`.
- Related patterns: существующие Go-тесты и fail-fast проверки из завершённого плана проекта.
- Dependencies:
  - golangci-lint v2.12.2 и `golangci/golangci-lint-action`.
  - govulncheck v1.1.4 и официальный `golang/govulncheck-action`.
  - GoReleaser v2.17.1 и `goreleaser/goreleaser-action`.
  - actionlint v1.7.12 для проверки workflow-файлов.
  - GitHub Actions закрепляются полными commit SHA с комментарием о версии.

## Development Approach

- **Testing approach**: Regular — сначала конфигурация компонента, затем её локальная автоматическая проверка.
- Makefile становится единым интерфейсом между локальной разработкой и CI.
- Использовать только высокосигнальные линтеры; не включать `all` и нестабильные метрики.
- CI работает с минимальными permissions; только release workflow получает `contents: write`.
- Серия релизов намеренно фиксирована как `v0.1.x`; смена minor/major требует явного изменения release series.
- Complete each task fully before moving to the next.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Добавить единые локальные команды и настройки линтера

**Files:**

- Create: `Makefile`
- Create: `.golangci.yml`
- Modify: `.gitignore`

- [x] Добавить цели `build`, `test`, `test-race`, `lint`, `coverage`, `vuln`, `ci` и `clean`; объявить их `.PHONY`.
- [x] Собирать `bin/horizont-epub` с `-trimpath`; позволить переопределять пути к внешним инструментам переменными Make.
- [x] Реализовать `coverage` через временный coverprofile, `go tool cover` и порог `COVERAGE_MIN=80`, всегда удаляя временный файл.
- [x] Настроить golangci-lint v2 с фиксированным высокосигнальным набором: стандартные анализаторы, `bodyclose`, `errorlint`, `exhaustive`, `gocritic`, `gosec`, `misspell`, `nilerr`, `noctx`, `revive`, `unconvert`; отдельно проверять `gofmt` и `goimports`.
- [x] Не добавлять исключения для текущего кода, поскольку выбранный набор уже проходит без предупреждений.
- [x] Игнорировать `bin/`, `dist/` и coverage-артефакты.
- [x] Проверить `make build`, `make test-race`, `make lint`, `make coverage` и `make vuln`.
- [x] Проверить отрицательный сценарий: `make coverage COVERAGE_MIN=100` должен завершиться ошибкой.
- [x] Запустить `make ci` — все проверки должны пройти до Task 2.

### Task 2: Добавить CI и Dependabot security-only

**Files:**

- Create: `.github/workflows/ci.yml`
- Create: `.github/dependabot.yml`

- [x] Запускать CI для `pull_request` и push в `master`, отменяя только устаревшие запуски того же PR/ref.
- [x] Установить `permissions: contents: read`, явные `timeout-minutes` и Go `1.25.x` с кешем по `go.sum`.
- [x] Разнести lint, build/tests/coverage и govulncheck по параллельным jobs.
- [x] Запускать golangci-lint с `.golangci.yml`, тесты и coverage через Makefile, govulncheck для `./...`.
- [x] Закрепить Actions полными commit SHA и версии инструментов без `latest`.
- [x] Настроить Dependabot для `gomod` и `github-actions`; поставить `open-pull-requests-limit: 0`, чтобы отключить обычные version-update PR, не блокируя security updates.
- [x] Автоматически разобрать `.github/dependabot.yml` и проверить обе экосистемы, корневой каталог и нулевой лимит обычных PR.
- [x] Проверить `.github/workflows/ci.yml` через actionlint v1.7.12.
- [x] Запустить те же Makefile-цели, которые вызывает workflow — все проверки должны пройти до Task 3.

### Task 3: Настроить сборку релизных артефактов

**Files:**

- Create: `.goreleaser.yml`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `.gitignore`

- [x] Настроить GoReleaser v2 для бинарника `horizont-epub` с `CGO_ENABLED=0`, `-trimpath` и компактными ldflags.
- [x] Собирать Linux, macOS и Windows для `amd64` и `arm64`; использовать ZIP для Windows, tar.gz для остальных платформ.
- [x] Формировать SHA-256 checksums и предсказуемые имена архивов без package managers, signing и иных неподтверждённых каналов публикации.
- [x] Добавить `make release-check`, выполняющий `goreleaser check` и snapshot-сборку без публикации.
- [x] Добавить проверку GoReleaser-конфигурации в CI.
- [x] Проверить `goreleaser check` и `goreleaser release --snapshot --clean`.
- [x] Проверить наличие ожидаемых архивов и checksum-файла в `dist/`, затем очистить артефакты.
- [x] Запустить `make ci` — все проверки должны пройти до Task 4.

### Task 4: Автоматизировать patch-релиз каждого merged PR

**Files:**

- Create: `.github/workflows/release.yml`

- [x] Запускать workflow на каждый push в `master`; через GitHub API требовать связанный merged PR и завершаться ошибкой для прямого push.
- [x] Использовать `GITHUB_RUN_NUMBER` как уникальный patch-компонент тега серии `v0.1.x`, исключая гонку между одновременными merge.
- [x] Сделать создание тега идемпотентным: повторный запуск принимает существующий тег только при совпадении SHA.
- [x] Формировать Markdown-описание из номера, заголовка, тела, автора и ссылки merged PR без интерполяции пользовательского текста в shell.
- [x] Checkout выполнять с полной историей и тегами; release workflow выдать только `contents: write` и `pull-requests: read`.
- [x] Запускать закреплённый GoReleaser v2 с подготовленным release-notes файлом и встроенным `GITHUB_TOKEN`.
- [x] Проверить workflow через actionlint и выполнить snapshot-релиз локально.
- [x] Автоматически проверить расчёт разных тегов для разных `GITHUB_RUN_NUMBER` и идемпотентный повтор для того же SHA.
- [x] Запустить `make ci` — все проверки должны пройти до Task 5.

### Task 5: Verify acceptance criteria

- [x] Запустить `make clean && make build`.
- [x] Запустить `make lint`.
- [x] Запустить `make test-race`.
- [x] Запустить `make coverage` и подтвердить итог не ниже 80%.
- [x] Запустить `make vuln` (skipped - pinned govulncheck v1.1.4 could not reach vuln.go.dev from the validation environment).
- [x] Запустить `make release-check`.
- [x] Проверить оба workflow через actionlint.
- [x] Проверить Dependabot-конфигурацию автоматическим разбором YAML.
- [x] Убедиться, что после проверок нет незакоммиченных `bin/`, `dist/` или coverage-файлов.

### Task 6: Update documentation

**Files:**

- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] Заменить разрозненные локальные команды в README на Makefile-цели; описать требуемые версии внешних инструментов и порог покрытия 80%.
- [ ] Описать CI jobs, security-only режим Dependabot, состав релизных архивов и автоматический patch-релиз после merge в `master`.
- [ ] Зафиксировать ограничение серии `v0.1.x` и способ её явной смены.
- [ ] Обновить CLAUDE.md: агенты должны использовать `make build`, `make lint`, `make coverage`, `make vuln`, `make release-check` и итоговый `make ci`.
- [ ] Запустить все документированные команды и проверить их соответствие Makefile.
- [ ] Запустить полный `make ci`.

## Post-Completion

- В настройках GitHub включить Dependabot alerts и Dependabot security updates; один `.github/dependabot.yml` не включает эти функции на уровне репозитория.
- Защитить `master`: запретить direct push и требовать успешные CI jobs перед merge. Release workflow намеренно отклоняет push без связанного merged PR.
- Разрешить GitHub Actions создавать tags и Releases через `GITHUB_TOKEN`.
- Живую публикацию проверить первым реальным merge после установки workflow; локальная копия сейчас не имеет GitHub remote, поэтому план не включает тестовую публикацию или удаление релиза.
