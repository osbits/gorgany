# Security policy

## Reporting a vulnerability

Do not open a public issue. Write to <security contact> with the affected version
(`/healthz` reports it), the steps to reproduce, and the impact you expect. You get an
answer within two working days.

## Supported versions

Only the version running in production. Fixes ship as a new release.

## Remediation records

Every report, review and fix is recorded in `docs/security/YYYY-MM-DD-<topic>.md`:
what was found, its severity, the fix, and the regression test that now covers it. The
records are tracked like code.
