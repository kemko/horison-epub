# Исправление Windows-сборки EPUB и govulncheck

## Overview

Исправить две причины падения GitHub Actions: несовместимость `go-epub` MemoryFS с Windows и сканирование разных версий стандартной библиотеки локально и в CI. Зафиксировать Go 1.26.6 как единый baseline. Не добавлять тесты для YAML или Makefile.

## Context

- Files involved: `epub.go`, `epub_test.go`, `main_test.go`, `go.mod`, `Makefile`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `DEVELOPMENT.md`.
- На Windows `go-epub` v1.2.1 сочетает `filepath` с POSIX-ориентированной MemoryFS. Созданные через обратную косую черту файлы не попадают в обход дерева, поэтому итоговый ZIP содержит только часть EPUB.
- `TestWriteEPUBRejectsOutputDirectoryWritableByOthers` ошибочно проверяет POSIX-права на Windows, хотя production-код намеренно их игнорирует.
- Локальный govulncheck анализировал Go 1.26.6, а `setup-go` выбрал Go 1.25.12. Govulncheck берёт версию стандартной библиотеки из `GOVERSION`, поэтому результаты различались.
- Перечисленные уязвимости исправлены начиная с Go 1.25.13; зафиксированный Go 1.26.6 также содержит исправления.
- `setup-go` будет читать точную версию из `go.mod`, а Makefile — передавать тот же baseline в govulncheck.
- Новые зависимости не нужны. На Windows `go-epub` будет использовать системный временный каталог; на остальных ОС останется MemoryFS.

## Development Approach

- **Testing approach**: Regular — сначала минимальное исправление, затем корректировка существующих regression-тестов.
- Не создавать Go-тесты для Makefile или GitHub Actions. Проверять их выполнением `make vuln` и `actionlint`.
- Существующие Windows end-to-end тесты уже воспроизводят потерю EPUB-файлов; дополнительные дублирующие тесты не нужны.
- Полностью завершать и проверять каждую задачу перед следующей.
- Все изменённые Go-тесты должны проходить перед настройкой toolchain.

## Implementation Steps

### Task 1: Исправить генерацию EPUB и платформенные тесты на Windows

**Files:**

- Modify: `epub.go`
- Modify: `epub_test.go`
- Modify: `main_test.go`

- [x] В `newEPUB` выбирать `goepub.OsFS` только для Windows, сохранив MemoryFS на остальных ОС.
- [x] Сохранить сериализацию через `epubBuildMu`, поскольку backend библиотеки остаётся process-global.
- [x] Обновить `TestNewEPUBDoesNotUseSharedTemporaryStorage`: проверять MemoryFS только на поддерживаемых ОС и явно учитывать Windows fallback.
- [x] Явно пропускать `TestWriteEPUBRejectsOutputDirectoryWritableByOthers` на Windows; платформенную логику продолжит проверять `TestDirectoryModeUnsafeIsPlatformSpecific`.
- [x] Убедиться, что существующие EPUB/end-to-end тесты проверяют полный ZIP, manifest, навигацию и отсутствие обратных косых черт; не добавлять дублирующий тест, если текущих проверок достаточно.
- [x] Запустить `make test` и `make test-race`; оба должны пройти перед Task 2.

### Task 2: Зафиксировать Go 1.26.6 и синхронизировать govulncheck

**Files:**

- Modify: `go.mod`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

- [ ] Зафиксировать baseline `go 1.26.6` в `go.mod`.
- [ ] Добавить в Makefile переопределяемую версию сканирования, по умолчанию получаемую из `GoVersion` главного модуля, нормализовать её до `go1.26.6` и передавать govulncheck через `GOVERSION`.
- [ ] Проверить диагноз командой `make vuln VULN_GO_VERSION=go1.25.12`: она должна завершиться ненулевым кодом и показать перечисленные уязвимости стандартной библиотеки.
- [ ] Проверить исправленный baseline обычной командой `make vuln`: сканирование Go 1.26.6 должно пройти.
- [ ] Во всех jobs `ci.yml` и в `release.yml` заменить диапазон `1.25.x` на `go-version-file: go.mod`, чтобы локальная проверка, CI и release использовали один источник версии.
- [ ] Не добавлять отдельные тесты для Makefile/workflows; запустить `actionlint .github/workflows/ci.yml .github/workflows/release.yml`.
- [ ] Запустить `make test`; он должен пройти перед Task 3.

### Task 3: Документировать причину и выполнить полный набор проверок

**Files:**

- Modify: `DEVELOPMENT.md`

- [ ] Обновить требуемую версию Go до 1.26.6.
- [ ] Описать, что `make vuln` сканирует baseline из `go.mod`, а `VULN_GO_VERSION` позволяет диагностически проверить другую версию.
- [ ] Зафиксировать Windows fallback на системный временный каталог и условие его удаления: исправленная cross-platform MemoryFS в новой версии `go-epub`.
- [ ] Не менять README: поведение пользовательского CLI не изменяется.
- [ ] Запустить `make build`.
- [ ] Запустить `make lint`.
- [ ] Запустить `make coverage` и подтвердить покрытие не ниже 80%.
- [ ] Запустить `make vuln`.
- [ ] Запустить `make release-check`.
- [ ] Повторно запустить `actionlint .github/workflows/ci.yml .github/workflows/release.yml`.
- [ ] Запустить итоговый `make ci`; все проверки должны пройти.

## Post-Completion

После публикации GitHub Actions должны подтвердить выполнение EPUB-тестов на Windows и govulncheck с Go 1.26.6. Отдельная тестовая инфраструктура для CI не требуется.
