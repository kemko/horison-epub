# Разработка horisont-epub

## Требования

Для сборки требуется Go 1.25 или новее.

Для полного набора локальных проверок также нужны:

- goimports v0.37.0;
- golangci-lint v2.12.2;
- govulncheck v1.1.4;
- GoReleaser v2.17.1;
- actionlint v1.7.12 — при изменении GitHub Actions.

## Сборка и проверки

Makefile служит единым интерфейсом для локальных и CI-проверок:

```sh
make build
```

Бинарник создаётся как `bin/horisont-epub`. Доступные цели:

```text
make build         Сборка бинарника
make test          Обычные тесты
make test-race     Тесты с race detector
make lint          gofmt, goimports и golangci-lint
make coverage      Покрытие с минимальным порогом 80%
make vuln          Проверка govulncheck
make release-check Проверка GoReleaser и snapshot-артефактов
make ci            Полный локальный CI-набор
make clean         Удаление bin/, dist/ и coverage-артефактов
```

Порог покрытия можно переопределить, например `make coverage COVERAGE_MIN=85`.
Путь бинарника задаёт `BINARY`; команды инструментов можно переопределить через
`GO`, `GOFMT`, `GOIMPORTS`, `GOLANGCI_LINT`, `GOVULNCHECK` и `GORELEASER`.
`make ci` запускает build, race-тесты, lint, coverage, vuln и release-check.

При изменении workflow-файлов запустите:

```sh
actionlint .github/workflows/ci.yml .github/workflows/release.yml
```

## Ограничения и гарантии

Сборка выпуска идёт последовательно и прекращается при первой ошибке загрузки
или разбора выпуска, статьи либо изображения. EPUB сначала записывается во
временный файл в каталоге назначения и публикуется только после успеха.
Существующий файл не перезаписывается.

Ход работы и ошибки выводятся в `stderr`. `stdout` сохраняется пригодным для
скриптов и при успехе содержит только путь опубликованного EPUB. Точные
формулировки сообщений не являются публичным контрактом.

Поддерживаются JPEG, PNG, GIF и статические SVG без активного содержимого и
внешних ресурсов. Формат определяется по содержимому; MIME сервера и расширение
URL, если они есть, также должны ему соответствовать. HTML ограничен 16 MiB,
изображение — 32 MiB и 40 млн пикселей. Для GIF лимит пикселей применяется ко
всем кадрам вместе, число кадров ограничено 1000. Один выпуск ограничен 500
материалами, 2000 HTTP-запросами, 512 MiB загруженных данных и 512 млн
декодированных пикселей. Повторяющиеся URL материалов загружаются один раз.

Загрузчик по умолчанию не подключается к loopback, private и link-local адресам,
включая перенаправления, и не использует proxy из переменных окружения. Каждый
HTTP-запрос ограничен 60 секундами, цепочка — 10 перенаправлениями. Для
доверенного выпуска во внутренней сети ограничение можно явно снять флагом
`-allow-private-network`.

На Unix каталог назначения не должен быть доступен для записи группе или другим
пользователям. На Windows синтетические POSIX-биты не проверяются.

## CI

GitHub Actions запускает CI для каждого pull request и push в `master`. Jobs
выполняются параллельно:

- `lint`: форматирование, actionlint и golangci-lint;
- `test`: сборка, обычные и race-тесты, coverage с порогом 80%;
- `windows-test`: сборка и тесты генерации EPUB на Windows;
- `vuln`: govulncheck для `./...`;
- `release-check`: проверка GoReleaser и snapshot-сборка без публикации.

Для `gomod` Dependabot работает в security-only режиме: обычные version-update
PR отключены (`open-pull-requests-limit: 0`), security updates этим лимитом не
блокируются. Для закреплённых SHA GitHub Actions включены еженедельные version
updates, поскольку Dependabot не создаёт security alerts для SHA-ссылок. В
настройках репозитория нужно отдельно включить Dependabot alerts и security
updates.

## Релизы

После успешного CI для push в `master` release workflow требует ровно один
соответствующий merged pull request в `master`, создаёт идемпотентный тег серии
`v0.1.x` с patch-компонентом из номера исходного CI run, формирует release notes
из этого PR и публикует артефакты через GoReleaser. Для прямого push release job
завершится ошибкой, но сам push не отменится.

В настройках GitHub нужно запретить прямой push в `master`, требовать успешные CI
jobs перед merge и разрешить `GITHUB_TOKEN` создавать tags и Releases.

Для Linux и macOS выпускаются tar.gz, для Windows — ZIP. Набор включает `amd64`
и `arm64` для каждой ОС, а также SHA-256 checksums. Бинарники собираются без CGO.

Серия релизов намеренно ограничена `v0.1.x`. Для перехода на другую minor или
major серию нужно явно изменить шаблон тега в `scripts/release-tag.sh` и эту
документацию.
