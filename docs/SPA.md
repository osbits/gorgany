# Serving a built SPA

`PublicController` serves `/public/*` and answers `400` for everything else, so
client-side routing could not work: a deep link — a path the server has no route for,
which the client router would have handled — got a `400`. Reloading `/settings/profile`
in a Gorgany-hosted SPA failed.

`SpaController` serves real files where they exist and the entry document everywhere else.

## Using it

```go
routeProvider.AddController(controller.NewSpaController("web/dist"))
```

Mount it **last**. It claims a catch-all pattern, so anything registered after it is
unreachable — the duplicate-route check will tell you if you get that wrong.

Defaults:

| Field | Default | Meaning |
|-------|---------|---------|
| `Root` | *(required)* | The directory holding the built app |
| `Pattern` | `/*` | Route to mount on |
| `IndexFile` | `index.html` | Entry document served for an unmatched path |
| `ExcludedPrefixes` | `/api/`, `/api`, `/public/`, `/csrf` | Paths that must never fall back |
| `ImmutablePattern` | hashed-filename regex | Which assets can be cached forever |

## What must not fall back

`ExcludedPrefixes` matters more than it looks. Without it, a typo'd API call — `GET
/api/v1/widget` instead of `/widgets` — returns `200` and an HTML document, and the
client's JSON parse fails somewhere far away from the cause. **An API 404 must stay a
404.**

A prefix ending in `/` matches by prefix. One without matches the exact path or a path
with a slash after it, so `/api` excludes `/api` and `/api/v1` but not `/apiary`.

Setting `ExcludedPrefixes` replaces the defaults rather than adding to them. To keep them:

```go
spa.ExcludedPrefixes = append(controller.DefaultSpaExclusions, "/graphql", "/metrics")
```

An explicitly empty slice means no exclusions, which is different from leaving the field
nil.

## Caching

| What | `Cache-Control` |
|------|-----------------|
| The entry document, however reached | `no-store, must-revalidate` |
| An asset with a content hash in its name | `public, max-age=31536000, immutable` |
| Any other asset | `public, max-age=300` |

The entry document is never cached because it is the one file naming the current asset
hashes: a stale copy points at assets that no longer exist, which is the classic
white-screen-after-deploy.

Only *hashed* names get immutable caching. `DefaultImmutableAssetPattern` matches what
modern bundlers emit — `app.4f3a91c2.js`, `main-a1b2c3d4e5.css`, `chunk.9f8e7d6c.mjs` —
and deliberately does not match `logo.png` or `app.js`. Pinning a hash-free file in every
visitor's browser for a year makes replacing it impossible.

Override with `ImmutablePattern` if your bundler names things differently.

## Content types

Resolved from the extension via `mime.TypeByExtension`, with `.mjs`, `.cjs`,
`.webmanifest` and `.map` handled explicitly — `mime`'s table misses them, and serving an
ES module as `application/octet-stream` makes the browser refuse to execute it.

## Path traversal

Requests are refused if they escape `Root`. The check is `filepath.Clean`, a `..`
rejection, and an absolute-path containment test — the last one being what actually
enforces the boundary, and it compares with the separator appended so
`/srv/web-dist-backup` is not treated as inside `/srv/web-dist`.

## Misconfiguration

- No `Root`: `500`, saying so. Falling back to the working directory is how a whole source
  tree gets published.
- `Root` set but no entry document: `404` naming the path it looked for and asking whether
  the app is built. That is almost always the actual problem.
