# CLAUDE.md

## Архитектура

- `main.go` отвечает за CLI, последовательную оркестрацию и атомарную публикацию результата без перезаписи.
- `parser.go` разбирает и очищает WordPress HTML; `BuildEPUB` получает уже очищенные статьи.
- `fetch.go` ограничивает HTTP-ответы, проверяет изображения, дедуплицирует их и управляет временными файлами.
- `epub.go` встраивает ресурсы и формирует EPUB 3 с вложенной навигацией.

## Проверка

Используй Makefile как единый интерфейс локальных и CI-проверок:

```sh
make build
make test
make test-race
make lint
make coverage
make vuln
make release-check
make ci
make clean
```

Перед завершением изменений обязательно запускай `make build`, `make lint`,
`make coverage`, `make vuln`, `make release-check` и итоговый `make ci`.
Минимальный порог покрытия — 80%. Версии инструментов указаны в README.
