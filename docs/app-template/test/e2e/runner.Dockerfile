# The e2e runner: the test sources baked into an image, so the suite runs the same way
# on a laptop and under docker-in-docker, where a bind mount of the checkout would
# resolve on the daemon's filesystem and come up empty.
# Its ignore file is runner.Dockerfile.dockerignore; the app image's allowlist would
# strip the tests.
ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION}
ENV GOTOOLCHAIN=local GOFLAGS="-mod=readonly -buildvcs=false"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test -tags=e2e -count=1 -run '^$' ./test/e2e/...
CMD ["go", "test", "-tags=e2e", "-count=1", "-v", "./test/e2e/..."]
