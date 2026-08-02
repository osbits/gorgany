FROM golang:1.26.5

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV GOFLAGS=-buildvcs=false
