# horizont-epub

CLI для сборки выпусков «Горизонта» в автономный EPUB 3.

Требуется Go 1.25 или новее.

Для локальных проверок также нужны goimports v0.37.0, golangci-lint v2.12.2,
govulncheck v1.1.4 и GoReleaser v2.17.1. Для проверки GitHub Actions нужен
actionlint v1.7.12.

## Сборка и проверка

```sh
make build
```

Бинарник создаётся как `bin/horizont-epub`. Доступны следующие цели:

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
`make ci` запускает build, race-тесты, lint, coverage, vuln и release-check.

Для локальной проверки workflow-файлов:

```sh
actionlint .github/workflows/ci.yml .github/workflows/release.yml
```

## Использование

```text
bin/horizont-epub [-o output.epub] <issue-url>
```

Без `-o` имя результата берётся из последнего сегмента пути URL. Например:

```sh
bin/horizont-epub https://astra-nova.org/issues/horisont/horisont-n-82/
```

создаёт `horisont-n-82.epub` в текущем каталоге. Пользовательский путь задаётся явно:

```sh
bin/horizont-epub -o books/horisont-n-82.epub https://astra-nova.org/issues/horisont/horisont-n-82/
```

Каталог назначения должен существовать заранее, не быть доступным для записи группе или другим пользователям; утилита не создаёт отсутствующие каталоги.

Сборка идёт последовательно и прекращается при первой ошибке загрузки или разбора выпуска, статьи либо изображения. EPUB сначала записывается во временный файл в каталоге назначения и публикуется только после успеха. Существующий файл не перезаписывается.

Поддерживаются встроенные изображения JPEG, PNG, GIF и статические SVG без активного содержимого и внешних ресурсов. Формат определяется по содержимому; если сервер прислал MIME или URL содержит расширение, они также должны соответствовать содержимому. HTML ограничен 16 MiB, изображение — 32 MiB и 40 млн пикселей; для GIF лимит пикселей применяется ко всем кадрам вместе, число кадров ограничено 1000.

Видео, `iframe` и интерактивные WordPress-блоки в EPUB не включаются. Новые форматы изображений следует добавлять только при их появлении в выпусках и с отдельной проверкой формата.

## CI

GitHub Actions запускает CI для каждого pull request и для push в `master`.
Jobs выполняются параллельно:

- `lint`: форматирование и golangci-lint;
- `test`: сборка, обычные и race-тесты, coverage с порогом 80%;
- `vuln`: govulncheck для `./...`;
- `release-check`: проверка GoReleaser и snapshot-сборка без публикации.

Dependabot работает в security-only режиме для `gomod` и `github-actions`.
Обычные version-update PR отключены (`open-pull-requests-limit: 0`), security
updates не блокируются этим лимитом.

## Релизы

Каждый push в `master` должен соответствовать merged pull request. Release
workflow отклоняет прямой push, создаёт идемпотентный тег серии `v0.1.x` с
patch-компонентом из `GITHUB_RUN_NUMBER`, формирует release notes из merged PR и
публикует артефакты через GoReleaser.

Для Linux и macOS выпускаются tar.gz, для Windows — ZIP. Набор включает
`amd64` и `arm64` для каждой ОС, а также SHA-256 checksums. Бинарники
собираются без CGO.

Серия релизов намеренно ограничена `v0.1.x`. Для перехода на другую minor или
major серию нужно явно изменить шаблон тега в `scripts/release-tag.sh` и эту
документацию.

Проверка проекта без публикации:

```sh
make ci
```
