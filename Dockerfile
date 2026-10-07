# Build the workout-tracker server and ship it as a tiny static image.
#
# The binary is pure Go (CGO_ENABLED=0), so it runs on the distroless static
# base with no shell or package manager. The image runs as the nonroot user.
#
# Build:  docker build -t ghcr.io/faarihihsan/kilo-be:dev .
# The version is injected like the Makefile does for local builds.
# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
WORKDIR /src

# Cache module downloads: the go.mod/go.sum layer only changes when deps do.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server

# The app listens on HTTP_ADDR (127.0.0.1:8080 behind nginx); EXPOSE is
# documentation only.
EXPOSE 8080

ENTRYPOINT ["/server"]
