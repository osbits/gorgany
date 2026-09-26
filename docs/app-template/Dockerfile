# syntax=docker/dockerfile:1
# GO_VERSION must equal go.mod's toolchain directive; test/arch enforces it.
ARG GO_VERSION=1.26.5

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
# GOTOOLCHAIN=local: never download a second toolchain in the middle of a build.
ENV GOTOOLCHAIN=local CGO_ENABLED=0
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Package builds, not file builds: each directory name becomes a binary name.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X $(go list -m)/pkg/buildinfo.Version=${VERSION}" \
      -o /out/ ./cmd/app ./cmd/cli
# The runtime filesystem: a non-root user, a sticky /tmp, and an empty .env, because
# the framework refuses to start without one. Real values arrive as environment
# variables, which the empty file never overrides.
RUN mkdir -p /rootfs/etc /rootfs/tmp /rootfs/app \
 && chmod 1777 /rootfs/tmp \
 && touch /rootfs/app/.env \
 && printf 'app:x:65532:65532::/app:/sbin/nologin\n' > /rootfs/etc/passwd \
 && printf 'app:x:65532:\n' > /rootfs/etc/group \
 && mkdir -p /writable/resource/temp /writable/public/storage

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /rootfs/ /
WORKDIR /app
COPY --from=build /out/app /out/cli /app/
COPY --from=build /src/config/ /app/config/
COPY --from=build /src/resource/ /app/resource/
# The only directories the app user may write: resource/temp (upload temp copies)
# and resource/public/storage. The framework stores File.Write(p, …) under
# resource/public/<p>, so every p starts with "storage/"; any other p fails with
# "permission denied" here. Mount the production volume on resource/public/storage;
# an empty named volume inherits this owner.
COPY --from=build --chown=65532:65532 /writable/resource/ /app/resource/
COPY --from=build --chown=65532:65532 /writable/public/ /app/resource/public/
USER 65532:65532
ENV MODE=prod
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s CMD ["/app/app", "healthcheck"]
# CMD, not ENTRYPOINT: `docker compose run --rm app /app/cli db:migrate up` must
# replace the command, not pass its arguments to the server.
CMD ["/app/app"]
