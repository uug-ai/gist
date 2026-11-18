# gist.uug.ai

Share code and text snippets in one click, in the spirit of GitHub Gist.
Paste, create a secret gist, share the link. No account needed. Every gist is
secret: never listed or indexed, only visible to people with its link.

- **Production:** https://gist.uug.ai
- **Staging:** https://gist.uug.lol
- **Plan and comparison with GitHub Gist:** [docs/plan.md](docs/plan.md)

## Features

- Gists with a description and up to 20 files (1 MiB in total).
- Every gist is **secret**, like GitHub's secret gists. There are no public
  gists and no Discover page, by design.
- Syntax highlighting for 250+ languages, rendered Markdown, line numbers and
  line links (`#file-main-go-L12`), light and dark.
- Edit with revisions and diffs, delete, fork, download as ZIP, raw files.
- Optional expiry: 1 hour, 1 day, 1 week or 30 days.
- Terminal uploads and a JSON API shaped like GitHub's, documented on
  [/terminal](https://gist.uug.ai/terminal).

Ownership works with an **edit key**: creating a gist sets it as a cookie in
your browser (scoped to that gist) and shows it under *Edit key* on the gist's
page. Anyone with the key can edit or delete the gist.

## From a terminal

```sh
curl -T main.go https://gist.uug.ai                         # share a file
make test 2>&1 | curl -T - "https://gist.uug.ai/?filename=test.log"
curl -F file=@main.go -F file=@go.mod \
     -F description="Repro for #42" -F expires=1w https://gist.uug.ai
curl https://gist.uug.ai/<id>                               # print a single-file gist
```

Uploads print the gist's URL; `curl -i` also shows the `Gist-Edit-Key`
header.

## API

| Method and path | |
|---|---|
| `POST /api/v1/gists` | Create. Body: `{"description": "", "files": {"a.py": {"content": "…"}}, "expires_in": "1d"}`. The response includes `edit_key`. `"public": true` is refused. |
| `GET /api/v1/gists/{id}` | Read the latest revision with file contents. |
| `PATCH /api/v1/gists/{id}` | Change the description and files, like GitHub: `{"files": {"old.txt": {"filename": "new.txt"}, "gone.txt": null}}`. |
| `DELETE /api/v1/gists/{id}` | Delete the gist with all its revisions. |

`PATCH` and `DELETE` need `Authorization: Bearer <edit key>`.

Other routes: `/terminal` (these instructions), `/{id}`, `/{id}/raw/{file}`, `/{id}/revisions`,
`/{id}/revisions/{n}`, `/{id}/revisions/{n}/raw/{file}`, `/{id}/archive.zip`.

## Configuration

| Variable | Default | |
|---|---|---|
| `PORT` | `8080` | Listen port. |
| `GIST_DATA_DIR` | `/data` | Where gists are stored. Must be writable; the server doesn't start otherwise. |
| `GIST_PUBLIC_URL` | from the request | The site's address, e.g. `https://gist.uug.ai`. Also decides whether cookies are `Secure`. |
| `GIST_TRUST_PROXY` | `false` | Read the client address from `X-Real-IP` / `X-Forwarded-For` (behind ingress-nginx). |
| `GIST_RATE_LIMIT` | `30` | Writes per client per minute; `0` turns the limit off. |
| `GIST_MAX_BYTES` | `1048576` | Largest revision, all files together. |
| `GIST_MAX_FILES` | `20` | Most files per gist. |
| `GIST_MAX_REVISIONS` | `100` | Most revisions per gist. |
| `GIST_STORE_QUOTA_BYTES` | `4294967296` | Total size of all gists and revisions. |
| `GIST_ADMIN_TOKEN` | unset | Optional, at least 32 characters: works as the edit key of every gist, to take down an abusive gist reported by link. |

## Development

```sh
GIST_DATA_DIR=./data PORT=8080 go run .
go test -race ./...
go run . --self-test
```

The code: `main.go` (configuration, self-test, shutdown), `internal/store`
(validation and JSON files on disk), `internal/render` (chroma, goldmark),
`internal/diff` (Myers line diff), `internal/server` (routes, templates,
static assets, API).

## Releases and deployment

1. Merge to `main` (the **Check** workflow runs tests and the image smoke test).
2. Actions → **Bump release** (`gh workflow run release-bump.yml -R uug-ai/gist -f bump=patch`)
   builds the hardened, signed `ghcr.io/uug-ai/gist` image for amd64 and
   arm64 and opens a gitops pull request for staging
   (`environments/staging/gist/deployment.yaml`).
3. Merge it; Argo CD (`gist-staging`, namespace `staging` on bijlokehof)
   rolls it out to gist.uug.lol.
4. Promote to production with a gitops pull request that changes the image in
   `environments/production/gist/deployment.yaml` (Argo CD `gist-production`,
   namespace `uug-ai`).

Staging loads the [inspect](https://inspect.uug.ai) bug report tool
(`static/inspect-loader.js` checks the hostname; production never loads it, and
only uug.lol hosts get the looser Content-Security-Policy it needs).
Its reports become issues in uug-ai/gist, which are public since the repo is.

Gists live on a Longhorn volume (`gist-data`) mounted at `/data`, so the
deployment runs one replica with the `Recreate` strategy.
