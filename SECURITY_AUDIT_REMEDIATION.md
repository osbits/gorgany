# Security audit remediation — SEC-H01 … SEC-H13

- **Audit date:** 2026-08-02 · **Remediation recorded:** 2026-08-02
- **Branch:** `security-review-2` · **Release:** v2.2.0 (supersedes the unreleased v2.0.0 tag)
- **Source audit:** `SECURITY_AUDIT_IMPLEMENTATION.md`

> **Note on the source documents.** `SECURITY_AUDIT_IMPLEMENTATION.md` and
> `SECURITY_RELEASE_BLOCKERS.md` were present as untracked files when this work began and
> were removed from the working tree before the first commit; they were never in git, so
> there is nothing to restore from. This file is therefore the remediation record and *not*
> a reconstruction of the audit. Each finding is summarised below in enough detail to know
> what the defect was, but the audit's own evidence, severity reasoning and acceptance
> criteria are not reproduced here — if a copy of either document survives elsewhere, it
> should be restored alongside this one.

## Status: all thirteen release blockers closed

| ID | Defect | Closed by |
|---|---|---|
| SEC-H01 | A stale replica restored revoked session state: the cache won over the row for everything but a *later* expiry, and every write re-sent the whole struct, so a routine heartbeat put back an attribute, an identity or a long expiry another replica had just cleared. | `GetSession` adopts the row; writes name only changed columns; state columns guarded on a new `version` column with attribute-operation replay on conflict. |
| SEC-H02 | Database-backed session mutations discarded persistence failures. `/csrf` handed out tokens the store never received. | Sticky failure on the entity, surfaced through the optional `core.ISessionWriteStatus`; recorded in the mediator, which is the one choke point every write passes. |
| SEC-H03 | Login could fail *after* issuing a live authenticated session — the cookie went out at session creation and nothing rolled back, so the user was told the login failed and was signed in on the next request. | Cookie emission split out of session creation; `Login` writes it only after the final durable write and revokes on any failure after that point. |
| SEC-H04 | `ISessionRevoker` was optional and the fallback reported every successful delete as "the session was live" — the one answer that disables the rotation guard. | Embedded in `core.ISessionStorage`; the fallback is gone. |
| SEC-H05 | A failed logout was recorded as a tombstone, so the next request minted a replacement and overwrote the client's only handle on the still-live session. | Pending revocations kept apart from tombstones, retried on the presenting request and by the sweep; replacement withheld per-identifier. |
| SEC-H06 | "Nobody is logged in" and "we could not find out" were one value, so a store outage served everyone as a guest — and guests were exempt from `RequiredRoles`. | Tri-state identity, fail-closed on failure, exemption deleted, guest DB filters read from the guest role's own bucket. |
| SEC-H07 | `GetAccessibleEntitiesWithFields` returned its input unfiltered after one type-level check. | Per-entity decision by the same predicate its sibling uses. |
| SEC-H08 | **Authorization SQL injection.** User attributes were substituted into `DBFilter.Field`, which is emitted as SQL. | Identifier hardening in `db/sql/core`; `Field` validated as an identifier; raw SQL moved to `RawSQL`/`RawArgs` with placeholders never expanded into them; emitter errors instead of dropping filters. |
| SEC-H09 | Owner and access-level rules read `Allowed` and ignored `RequiredRoles`, and the access level is a free-form application string. | One rule evaluator, explicit ten-row precedence, `OperationConfig.Deny`, `AllowedAccessLevels`. |
| SEC-H10 | `/public/*` served the whole `resource/` tree. | Anchored at `model.PublicStorage`, contained with `os.OpenRoot`, streamed with `http.ServeContent`; `PublicPath()` round-trips. |
| SEC-H11 | A multipart part naming an incompatible DTO field panicked *after* writing the upload — unauthenticated, repeatable disk fill. | `CanSet` and assignability checked before the part is opened; deferred cleanup guard covering error and panic. |
| SEC-H12 | The built-in browser login accepted an unprotected form POST. | `SameOriginMiddleware` on the login and logout routes, plus a shared form-parse seam so a middleware and the handler can both read the body. |
| SEC-H13 | Seventeen reachable vulnerabilities, a dead chi v1 module path, no CI. | Toolchain go1.26.5, dependencies bumped past the audit's minimums, chi migrated to /v5, CI created. |

Coupled Mediums closed in the same functions: **SEC-M02** (multipart limits), **SEC-M05**
(revocation lifetime, session-id length cap, cardinality bounds), **SEC-M11**
(`MaxFilters`/`MaxSorts`).

## Corrections to the audit

Checked against the code; the fixes are shaped by the corrected reading.

- **SEC-H01** — row *deletion* was already authoritative. The defect is in-row state.
- **SEC-H03** — the pre-login-CSRF-token sub-claim was already handled on the success path.
  The load-bearing defect is cookie ordering and the absent rollback.
- **SEC-H08** — only `Field` was exploitable; `Value`/`Values`/`Pattern` were already bound.
  Conversely the operator problem was *understated*: an unrecognised operator dropped the
  filter, and a dropped restriction is no restriction.
- **SEC-H12** — the claim that the CSRF middleware fails open with no session is **false**.
  It rejects, and an existing test pins that. Nothing was changed there.

## Defects found while implementing, not in the audit

`ResponseWriterWrapper`'s promoted `ReadFrom`/`WriteString`/`Flush`/`Hijack` nil-panicked and
blocked SEC-H10's streaming · `DBJoin` bound its right key as a value, so every join-based
RBAC filter compared a column to the *name* of the other column · `CustomFilters` role keys
were looked up lower-cased against maps every shipped example keys upper-case, so those
filters never matched · `getUserContextValue` defaulted an unknown key to the caller's user id
· `newSessionWithoutUser` orphaned its own row if the CSRF step failed · `isOwner` passed
`context.Background()` into application code and inferred ownership from a coincidental id
match · `AccessControlConfig.DefaultRoles` was dead config.

## A test that pinned a defect as intended

`auth/session_tombstone_test.go`'s `TestARevocationThatFailedIsStillRemembered` asserted the
refusal and stopped there — which was exactly SEC-H05. It is replaced by
`TestARevocationThatFailedIsRetriedNotJustRemembered`, keeping its three correct assertions
and adding the ones that make the refusal recoverable. Likewise
`TestStandardAuthStrategy_RotateSession` passed only *because* of SEC-H04's fail-open
fallback, and `db/sql/core/sql_conditions_test.go` asserted the rendering SEC-H08 exploited.

## Verification

```bash
go build ./...      # pass
go vet ./...        # pass
go test ./...       # pass
govulncheck ./...   # zero reachable vulnerabilities (was 17), no exception file
go test -race ./... # pass on every package this work touched
```

**Outstanding.** The live PostgreSQL and MySQL suites have **not** been executed. They are
wired into `.github/workflows/ci.yml` with `E2E_REQUIRE_LIVE=1` and an explicit assertion
that both engines ran — an all-skipped run reports `ok`, which is how a previous round's
twelve live cases were skipped under a criterion that read as passed. Until that job has run
green, this criterion is **not met and not attempted**.

Pre-fix failure output for each blocker's regression tests was captured during the work and
belongs in the pull request; every fix was verified to fail against the unfixed code before
being accepted.

## Where the changes are

`MIGRATION_v2.md` §35–§49 · `CHANGELOG.md` `[2.2.0]` · six commits on `security-review-2`.
