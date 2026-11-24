# portwatch-vi-rss

Serves RSS 2.0 and Atom feeds of the news on [PortWatch VI](https://portwatch-vi.xyz/),
built from the site's `data/tracker.json`.

The site has its own summary-only RSS feed (`/news/feed.xml`). This service adds:

- an Atom feed
- the full article: image, body and source links
- update times taken from the tracker's `updatedAt` field
- a stable order for items published on the same day

## Endpoints

| Path | Description |
|---|---|
| `/rss.xml` (also `/rss`, `/feed.xml`) | RSS 2.0 with `content:encoded` |
| `/atom.xml` (also `/atom`) | Atom 1.0 |
| `/healthz` | 200 once data is loaded, 503 before |
| `/` | Links to both feeds, with autodiscovery tags |

Feeds return an `ETag` and answer `If-None-Match` with 304. Upstream is polled
every `refresh_interval` with conditional requests. If a fetch fails, the
service keeps serving the last good copy and retries after 1 minute.

## Configuration

See [`config.yaml`](config.yaml). Every key is optional. Pass the file with `-config path`
(default `config.yaml`; in the container it is `/etc/portwatch-vi-rss/config.yaml`).

## Run

```sh
go run . -config config.yaml          # locally
docker compose up -d                  # pulls ghcr.io/ricardo-duarte-av/portwatch-vi-rss:latest
```

## CI

`.github/workflows/docker.yml` runs on every push. It runs vet and the tests, then builds a
multi-arch (amd64/arm64) image and pushes it to GHCR. Tags: `latest` (default branch),
the branch name, the short commit SHA, and semver tags for `v*` git tags. On pull requests
it builds the image without pushing it.
