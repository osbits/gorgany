# Rotate a secret

Rotate on a schedule, when someone with access leaves, and at once when a secret may
have leaked. Deleting a leaked value from git does not help: it stays in the history.

Every command below runs on the host from `/srv/myapp`, after
`export IMAGE=$(docker ps --filter label=com.docker.compose.service=app --format '{{.Image}}' | head -1)`,
the image that is running now.

## Database password (`DB_PASSWORD`)

1. At a quiet time, change it: `ALTER ROLE myapp PASSWORD '<new>';`. PostgreSQL keeps one
   password per role. Connections that are already open stay open, so the running app
   keeps serving, but every new connection needs the new value. Do steps 2 and 3 straight
   away.
2. Put the new value in `/etc/myapp/app.env`.
3. `docker compose -f compose.prod.yml up -d --wait app`: the new container connects with
   the new password.
4. Update any other environment that holds the old value.

## Signing keys (`JWT_SECRET`, when the app uses the `api` strategy)

Changing the key invalidates every token it signed, so every API client has to sign in
again. Announce it, rotate at a quiet time, and use at least 32 random bytes.

## The deploy key (`SSH_PRIVATE_KEY`)

Generate a new key pair, and add the public key to the deploy user's `authorized_keys`.
Update the CI variable with the new private key, encoded with `base64 -w0`. Run one
deploy, then remove the old public key.
