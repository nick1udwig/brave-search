# bravesearch

Simple Go CLI for Brave Search API, designed for agent-friendly usage.

## What It Does

- Calls Brave endpoints:
  - `GET /res/v1/web/search` (`search`)
  - `GET /res/v1/news/search` (`news`)
  - `GET /res/v1/images/search` (`images`)
  - `GET /res/v1/videos/search` (`videos`)
- Resolves API key in this order:
1. `--api-key`
2. `BRAVE_API_KEY` (or `BRAVE_SEARCH_API_KEY`)
3. `~/.brave-search/key`
- Stores config in `~/.brave-search/config.json`
- Stores cache in `~/.brave-search/cache/`
- Retries `429 Too Many Requests` automatically with backoff

## Install

### Prerequisites

- Go `1.23+`

### Install to `~/.local/bin` (recommended)

```bash
./scripts/install.sh
```

The installer builds `./cmd/bravesearch` and installs the binary at:

```bash
~/.local/bin/bravesearch
```

If `~/.local/bin` is not in your `PATH`, add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

You can also choose a custom location:

```bash
./scripts/install.sh --bin-dir /usr/local/bin
```

### Build Only (no install)

```bash
go build -o bravesearch ./cmd/bravesearch
```

### Uninstall

```bash
rm -f ~/.local/bin/bravesearch
```

## API Key Setup

Any one of these works:

```bash
./bravesearch search --api-key "$KEY" --q "golang"
```

```bash
export BRAVE_API_KEY="$KEY"
./bravesearch search --q "golang"
```

```bash
mkdir -p ~/.brave-search
printf '%s\n' "$KEY" > ~/.brave-search/key
chmod 600 ~/.brave-search/key
./bravesearch search --q "golang"
```

## Commands

### `search`

Run Brave web search.

```bash
./bravesearch search --q "site:go.dev context cancellation"
```

```bash
./bravesearch search --q "golang http client timeout" --count 5 --output titles
```

Useful flags:

- `--q`, `--query`: query text
- `--count`: 1-20
- `--offset`: 0-9
- `--country`, `--search-lang`, `--ui-lang`
- `--safesearch`: `off|moderate|strict`
- `--freshness`
- `--spellcheck` (default `true`)
- `--extra-snippets`
- `--summary`
- `--enable-rich-callback`
- `--result-filter`
- `--goggle` (repeatable)
- `--param key=value` (repeatable raw pass-through)
- `--output`: `json|pretty|urls|titles` (default `json`)
- `--no-cache`
- `--refresh`
- `--cache-ttl` (for example `30m`)
- `--timeout` (for example `20s`)
- `--api-version` (optional Brave `Api-Version` header)
- `--max-retries` (retry attempts on `429`, default `3`)

### `news`

Run Brave news search.

```bash
./bravesearch news --q "federal reserve inflation outlook" --count 10
```

`news` supports up to `--count 50`. It includes core flags plus:

- `--extra-snippets`
- `--goggle` (repeatable)

### `images`

Run Brave image search.

```bash
./bravesearch images --q "golden gate bridge fog" --count 30
```

`images` supports up to `--count 200`.

### `videos`

Run Brave video search.

```bash
./bravesearch videos --q "go concurrency patterns" --count 10
```

`videos` supports up to `--count 50`.

### `config`

Manage settings in `~/.brave-search/config.json`.

```bash
./bravesearch config init
./bravesearch config show
./bravesearch config get default_count
./bravesearch config set default_count 10
./bravesearch config set cache_ttl 30m
./bravesearch config paths
```

Supported keys:

- `base_url`
- `timeout`
- `cache_ttl`
- `cache_enabled`
- `default_country`
- `default_search_lang`
- `default_ui_lang`
- `default_safesearch`
- `default_count`
- `api_version`

### `cache`

Manage cache files in `~/.brave-search/cache/`.

```bash
./bravesearch cache stats
./bravesearch cache prune
./bravesearch cache clear
```

## Notes

- Default output is raw JSON for easier machine parsing.
- Cache key is based on request URL + API version.
- `--output urls|titles` is supported only for the `search` command.
