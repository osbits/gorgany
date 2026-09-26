# Deploy

## Once per host

1. Create the deploy user, and give it Docker access and `/srv/myapp`. Docker access is
   root-equivalent on the host, so the deploy key is a root credential for it.
2. Write the production env file, readable by the deploy user alone:
   `sudo install -d -m 750 -o deploy /etc/myapp && sudo install -m 600 -o deploy /dev/null /etc/myapp/app.env`.
   Fill in every variable in `docs/configuration.md` with production values, including
   `MODE=prod` and `DB_SSL=require` or stricter. Do not start from `.env.sample`: its
   development values, `MODE=dev` among them, would override the image's.
3. Log the deploy user in to the registry with a read-only deploy token:
   `docker login <registry>`.
4. Create the backup directory, readable by the deploy user alone:
   `sudo install -d -m 700 -o deploy -g deploy /var/backups/myapp`. A dump holds every row,
   password hashes and sessions included. Arrange for its contents to be copied off the
   host after every release.
5. In GitLab:
   - protect the `v*` tags (Maintainers only) as well as the default branch;
   - set the protected variables `DEPLOY_HOST`, `SSH_KNOWN_HOSTS` (the host's pinned key
     line) and `SSH_PRIVATE_KEY` (the deploy key, encoded with `base64 -w0`, masked, and
     used for nothing else);
   - register the protected, unprivileged runner tagged `deploy`.

## Normal release

CI's build and e2e jobs do step 1, and its `deploy` job does steps 2 to 6. Step 7 is
yours. To release by hand, run the same commands on the host from `/srv/myapp`, after
`export IMAGE=<registry>/<image>:<tag>`; `compose.prod.yml` refuses every command without it.

1. CI builds one image per pipeline and runs the e2e suite against it. Nothing else is
   ever deployed: never `:latest`, never a rebuild.
2. Copy `deploy/compose.prod.yml` to `/srv/myapp/`, and pull the image.
3. Back up the database: `docker compose -f compose.prod.yml run --rm backup`.
4. Migrate with the **new** image while the old version keeps serving:
   `docker compose -f compose.prod.yml run --rm migrate`. A failing migration exits
   non-zero and the release stops here.
5. Seed reference data: `docker compose -f compose.prod.yml run --rm seed`. Each seeder
   runs once per name, so this is a no-op when nothing is new.
6. Roll the app: `docker compose -f compose.prod.yml up -d --wait app`. `--wait` returns
   only when the container's HEALTHCHECK (`/app/app healthcheck` → `/readyz`) passes.
7. Check that `/healthz` reports the new version.

Because the old version serves while step 4 runs, every migration must work with the
previous release's code. Add new columns and tables in one release, and drop the ones only
the old code used in a later release.

## Rolling back

- Code only: re-run the `deploy` job of the previous release's pipeline. It deploys that
  pipeline's own image.
- A migration that dropped or rewrote data cannot be undone by `db:migrate down`.
  Restore the backup from step 3. The release's CHANGELOG deployment notes say when
  that applies.
