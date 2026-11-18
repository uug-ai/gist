# gist.uug.ai: plan

gist.uug.ai lets anyone share code and text snippets with one click, in the
spirit of [GitHub Gist](https://gist.github.com). This plan compares the two,
says what gist.uug.ai does today and what comes later.

## Principles

- **One click, no account.** Paste, press *Create gist*, share the link. From a
  terminal: `curl -T main.go https://gist.uug.ai`.
- **Secret by default, public by choice.** As on GitHub, every gist is
  *secret* unless you choose *public*: an unguessable 128-bit link, not
  listed, `noindex`. Public gists are listed on Discover and may be indexed.
- **Ownership by key, not by login.** Creating a gist returns an *edit key*.
  The browser keeps it in a cookie scoped to that gist; API and terminal users
  keep it themselves. Anyone with the key can edit or delete the gist.
- **Small and hardened.** One static Go binary on `scratch`, server-rendered
  pages, no database: gists are JSON files on a persistent volume. Released
  through the shared uug-ai hardened, signed release pipeline.

## Feature comparison

| GitHub Gist | gist.uug.ai | Notes |
|---|---|---|
| Create a gist with a description and several files | Yes | Up to 20 files, 1 MiB per revision. Empty filenames become `gistfile1.txt`, … |
| Secret gists (unlisted, anyone with the link) | Yes, the default | *Create secret gist* is the primary button, as on GitHub. 32-hex-character ids, `noindex`, never on Discover. |
| Public gists | Yes | *Create public gist*, `-F public=true`, or `"public": true`. Indexable at `/{id}`; revision pages stay `noindex`. |
| *Discover* (newest public gists) | Yes | `/discover`, 20 per page with a preview of the first file. |
| Change visibility | Yes, both ways | GitHub only lets a secret gist become public. Here the owner can also make a public gist secret again; it leaves Discover, but copies made meanwhile may remain. No new revision. |
| Forks keep the source's visibility | Yes | |
| Syntax highlighting by file extension | Yes | Server-side with [chroma](https://github.com/alecthomas/chroma), 250+ languages, light and dark. |
| Rendered Markdown | Yes | GFM via [goldmark](https://github.com/yuin/goldmark); raw HTML is dropped, links get `nofollow ugc`. |
| Line numbers and line links (`#file-x-L10`) | Yes | |
| Raw file URLs | Yes | `/{id}/raw/{file}` and per revision; served as `text/plain`, sandboxed. |
| Edit a gist | Yes | With the edit key (cookie or `Authorization: Bearer`). |
| Revisions with diffs | Yes | Every edit is a numbered revision; the revisions page shows unified diffs. |
| Delete | Yes | With the edit key. |
| Fork | Yes | One click; the fork links back to its source. |
| Download ZIP | Yes | `/{id}/archive.zip` |
| Copy contents / copy link | Yes | |
| REST API | Yes | `/api/v1/gists`, shaped like GitHub's (`files` map, `PATCH` semantics incl. renames and deletes). |
| Terminal upload | Yes (new) | `curl -T file`, `curl -F file=@a -F file=@b`, `… \| curl -T - …` |
| Expiry | Yes (new) | Optional: 1 hour, 1 day, 1 week, 30 days. |
| Embed (`<script src=….js>`) | Later | |
| Clone / push over git | Later | |
| Accounts, profile pages, stars, comments | Later | Sign in with GitHub, like calendar.uug.ai. Without accounts, "Your gists" lists the gists created in this browser. |
| Search | Later | Over public gists. |
| Reporting abuse | Later | Today: `GIST_ADMIN_TOKEN` works as every gist's edit key, so moderators can delete public gists via the API or the edit-key page. |

## Abuse and safety

- Per-client rate limit on writes (default 30 per minute) and a total storage
  quota (default 4 GiB) on the volume.
- Size limits enforced before anything is stored; content must be UTF-8 text.
- Strict Content Security Policy (no inline scripts or styles), `no-referrer`
  for outbound links so secret URLs don't leak, raw files sandboxed with
  `nosniff`, cross-origin form posts rejected (`http.CrossOriginProtection`).
- Edit keys are 256-bit random values; only their SHA-256 is stored.
- Public gists: links in rendered Markdown get `rel="nofollow ugc"`, so
  spamming Discover earns no search ranking. Moderators can take a gist down
  with the optional `GIST_ADMIN_TOKEN` (at least 32 characters).

## Architecture

```
main.go                 flags, config from env, --self-test, graceful shutdown
internal/store          validation, gists + revisions as JSON files (or memory)
internal/render         chroma highlighting, goldmark Markdown
internal/diff           Myers line diff and unified hunks
internal/server         routes, templates, static assets, rate limit, API
```

Storage layout on the volume (`GIST_DATA_DIR`, default `/data`):

```
gists/<id[0:2]>/<id>/meta.json     description, visibility, timestamps, key hash, revision list
gists/<id[0:2]>/<id>/r<N>.json     the files of revision N
```

Writes go to a temp file and are renamed into place. The Discover index of
public gists is kept in memory and rebuilt from the metadata at startup. One replica, `Recreate`
strategy, on a Longhorn volume (the same pattern as calendar).

## Delivery

1. Repository `uug-ai/gist`, cloned into the monorepo at `gist/`.
2. CI: `check.yml` (gofmt, vet, race tests, self-test, Docker build).
3. Release: `release-bump.yml` (*Bump release*) calls the shared
   `uug-ai/workflows` release pipeline: hardened `standalone` image, signed,
   multi-arch, pushed to `ghcr.io/uug-ai/gist`, and a gitops PR that bumps
   staging.
4. gitops: `environments/staging/gist` (gist.uug.lol, namespace `staging`) and
   `environments/production/gist` (gist.uug.ai, namespace `uug-ai`), with Argo
   CD Applications `gist-staging` and `gist-production` on bijlokehof.
   Production is promoted by a manual PR, like the other uug-ai sites.
5. DNS: `gist.uug.ai` and `gist.uug.lol` point at the bijlokehof ingress.

## Later

1. Sign in with GitHub; gists owned by accounts, a "Your gists" page across devices.
2. Search over public gists, a "Report" link and a moderation view.
3. Embeds, then git clone.
