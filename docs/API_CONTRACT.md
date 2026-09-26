# The API contract

An API without a contract gets documented by whatever is to hand, and every one of those
sources goes stale:

- **The generator's Postman collection goes stale.** `scheme/postman_scheme.json` is written
  only by `create-project` and `regenerate-project -whole`, so in practice it is produced once
  and left to drift. It also nests requests under `items`, a key Postman's collection format
  does not have; the format uses `item`.
- **The collection covers only generated CRUD**, so a large application's hand-written routes,
  often half of them, appear in no document at all.
- **Feature documents copy endpoint lists and response shapes**, and then describe an envelope
  the framework does not send.

This document says where the contract lives, what decides it, and which tests keep it true.
[app-template/api/](app-template/api/) has a working example.

## Where it lives

| File | What it is |
|------|------------|
| `api/openapi.yaml` | The HTTP contract, OpenAPI 3.1. Clients, reviewers and the tests read it |
| `api/routes.txt` | Every route the server answers: method, path, name and per-route middleware. It includes `GET /csrf`, which `RouteProvider` mounts unless `DisableCsrfController()` is called. `pkg/provider/routes_test.go` rewrites it with `-update`, and fails when it is stale |
| `api/asyncapi.yaml` | Outbound webhooks and events, if the application sends any |
| `api/examples/` | Request and response examples, referenced from the spec |
| `redocly.yaml` | The spec linter's rules, beside the Makefile |

`scheme/gorgany.json` is the generator's input. It describes the data model and access rules,
not the API: it has no operations, no error responses and no hand-written endpoints. If anyone
wants a Postman collection, generate it from `openapi.yaml` and never edit it. Delete the
generator's `scheme/postman_scheme.json`, and treat it as noise in review, because `-whole`
recreates it.

## What decides what

- **Code decides which routes exist.** The router serves the controllers passed to
  `RouteProvider`, plus `GET /csrf`, and it answers `OPTIONS` on every registered path (behind
  a CSRF filter, on every path the filter covers).
- **The spec decides their shape**: parameters, bodies, responses, errors, and who may call them.

The contract is not generated from the scheme. The generator produces only CRUD, and a real
application's hand-written routes are exactly the ones clients most need documented. Start
from the routes, not the scheme.

## Keeping it honest

| Check | Catches | Where |
|-------|---------|-------|
| `TestAPIRoutesMatchTheSpec` | A route that is served but not documented, or documented but not served | Unit tier, `pkg/provider/routes_test.go` |
| `TestRouteInventory` | A route change that reaches review without a visible diff; duplicate route names | Unit tier |
| `TestAPIRoutesRequireSignIn` | A route that is public by accident rather than by decision | Unit tier |
| Spec lint | A spec that is invalid or inconsistent | `make lint-api` (redocly, pinned) and its CI job |
| `oasdiff breaking <last release> api/openapi.yaml` | A breaking change inside `/api/v1` | Add it to CI, run against the last release tag |
| Response validation in e2e | A response that does not match its schema | Add it to `test/e2e`, for example with `openapi3filter`; the template does not do this yet |

The spec-parity test compares `METHOD /path` pairs. It renders a route the way a client sees
it: namespace `api` plus the path `/v1/notes/{id}` becomes `GET /api/v1/notes/{id}`, and a
pattern parameter such as `{id:[0-9]+}` is rendered as `{id}`.
`GET /csrf` sits outside the `api` namespace, so the spec's description mentions it rather than
documenting it as an operation.

An application adopting this with hundreds of undocumented routes can give the parity test an
allowlist of routes not yet in the spec. The test fails when the list gains a line, and when a
listed route appears in the spec, so the list can only shrink.

## The envelope

Every JSON response the framework writes through `dto.ReturnObject` has the same four keys:

```json
{
  "status": 404,
  "status_code": "NOT_FOUND",
  "body": null,
  "errors": ["note no-such-note not found"]
}
```

Define the envelope once, in `components`, and build every response from it:

```yaml
components:
  schemas:
    Envelope:
      type: object
      required: [status, status_code, body, errors]
      properties:
        status: { type: integer, description: "The HTTP status, repeated." }
        status_code: { type: string, examples: [SUCCESS, CREATED, NOT_FOUND, VALIDATION] }
        body: {}
        errors:
          type: [array, "null"]
          items: {}
    ValidationError:
      type: object
      required: [field, err]
      properties:
        field: { type: string }
        err: { type: string }
        rule: { type: string }
        param: { type: string }
        path: { type: string }
```

Quote any flow-mapping value that contains a comma. Unquoted, `The HTTP status, repeated.`
splits into a description and a stray key. The spec linter catches it; a reader does not.

An operation then narrows `body`:

```yaml
responses:
  "200":
    description: One note.
    content:
      application/json:
        schema:
          allOf:
            - $ref: "#/components/schemas/Envelope"
            - properties:
                body: { $ref: "#/components/schemas/Note" }
```

What each status means, so that every operation documents it the same way:

| Status | `status_code` | When |
|--------|---------------|------|
| 400 | `BAD_REQUEST` | The request body could not be parsed. That includes a body over `http.security.body.maxRequestBytes`: the framework answers 400, not 413 |
| 401 | `NOT_AUTHORIZED` | No valid session or token |
| 403 | `FORBIDDEN` | Authenticated, but the role or ownership check failed. Also a mutating request with a missing or wrong CSRF token, or one from another origin |
| 404 | `NOT_FOUND` | No such route, or no such entity |
| 405 | `METHOD_NOT_ALLOWED` | The path exists, but not for this method. The `Allow` header lists the methods it accepts |
| 422 | `VALIDATION` | The body parsed but failed validation, or a path or query parameter did not convert to the handler's parameter type (a path parameter is reported with the field `GeneralError`). `errors` holds `ValidationError`s ([VALIDATION.md](VALIDATION.md)) |
| 500 | `INTERNAL_ERROR` | Anything unclassified. Under `MODE=prod`, an error routed to the default handler carries only a generic message. A panic with a non-error value puts that value's text in `errors`, and an error handler that itself panics puts the original error's `Error()` text there. Both happen in every mode, so handlers panic with `error` values only, and error handlers must not panic |

**Filters answer before routing.** Behind a CSRF filter, such as the template's on `/api/**`,
a POST, PUT, PATCH or DELETE without a valid token gets `403` even when the path does not
exist or does not accept the method, and `OPTIONS` on any path the filter covers gets a bare
`204`. The 404 and 405 rows describe what a client with a valid token sees.

A form post from a browser gets a `303` back to the `Referer` instead of the `422`
(VALIDATION.md). The spec describes what API clients see.

## Versioning

- **`/api/v1` is the namespace `api` plus a `/v1` path prefix.** Keep the prefix in one constant.
  The template writes `constant.V1 + "/notes"`, never a literal `"/v1"`.
- **Within a version, changes are additive only.** A new optional field, a new operation, or a new
  enum value that clients may ignore is fine. A removed or renamed key, a new required field or
  a changed type is breaking. So is a key that is now omitted when it used to be sent as `null`.
- **A breaking change gets a new version**: new controllers in `pkg/controller/api/v2`, and a
  **Breaking** entry in the changelog. `v1` stays served until its clients have moved.
- **The spec's `info.version` follows the contract, not the binary.**

## Naming

- **Route names are `<namespace>.<resource>.<action>`** (`api.note.show`). A route outside a
  namespace drops that segment (`health.live`). The inventory test enforces uniqueness.
- **Use one path style per API version.** Generated CRUD uses lowerCamel resource segments. An
  application that starts with hand-written routes can choose kebab-case plural nouns instead.
  Mixing styles within one version is worse than either style. **Never rename a path in a
  released version**: it breaks every client that calls it.
- **`operationId`s are stable.** Generated clients use them as method names.
