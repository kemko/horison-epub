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
bin/horizont-epub [-allow-private-network] [-o output.epub] <issue-url>
```

Без `-o` имя результата берётся из последнего сегмента пути URL. Например:

```sh
bin/horizont-epub https://astra-nova.org/issues/horisont/horisont-n-82/
```

создаёт `horisont-n-82.epub` в текущем каталоге. Пользовательский путь задаётся явно:

```sh
bin/horizont-epub -o books/horisont-n-82.epub https://astra-nova.org/issues/horisont/horisont-n-82/
```

Каталог назначения должен существовать заранее; утилита не создаёт отсутствующие каталоги. На Unix каталог не должен быть доступен для записи группе или другим пользователям. На Windows синтетические POSIX-биты не проверяются.

Сборка идёт последовательно и прекращается при первой ошибке загрузки или разбора выпуска, статьи либо изображения. EPUB сначала записывается во временный файл в каталоге назначения и публикуется только после успеха. Существующий файл не перезаписывается.

Ход работы и ошибки выводятся в `stderr`. `stdout` сохраняется пригодным для скриптов и при успешной сборке содержит только путь опубликованного EPUB. В ходе работы отображаются загрузка выпуска, количество материалов, загрузка статей, сборка EPUB с изображениями и публикация результата. Точные формулировки сообщений не являются публичным контрактом.

Поддерживаются встроенные изображения JPEG, PNG, GIF и статические SVG без активного содержимого и внешних ресурсов. Формат определяется по содержимому; если сервер прислал MIME или URL содержит расширение, они также должны соответствовать содержимому. HTML ограничен 16 MiB, изображение — 32 MiB и 40 млн пикселей; для GIF лимит пикселей применяется ко всем кадрам вместе, число кадров ограничено 1000. Один выпуск ограничен 500 материалами, 2000 HTTP-запросами, 512 MiB загруженных данных и 512 млн декодированных пикселей; повторяющиеся URL материалов загружаются один раз.

Загрузчик по умолчанию не подключается к loopback, private и link-local адресам, включая перенаправления, и не использует proxy из переменных окружения. Для доверенного выпуска во внутренней сети это ограничение можно явно снять флагом `-allow-private-network`.

Видео, `iframe` и интерактивные WordPress-блоки в EPUB не включаются. Новые форматы изображений следует добавлять только при их появлении в выпусках и с отдельной проверкой формата.

## CI

GitHub Actions запускает CI для каждого pull request и для push в `master`.
Jobs выполняются параллельно:

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
`v0.1.x` с patch-компонентом из номера исходного CI run, формирует release notes из
этого PR и публикует артефакты через GoReleaser. Для прямого push release job
завершится ошибкой, но сам push не отменится.

В настройках GitHub нужно запретить прямой push в `master`, требовать успешные
CI jobs перед merge и разрешить `GITHUB_TOKEN` создавать tags и Releases.

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
