# Build stage: templ generation happens here so the image needs no committed
# generated code.
FROM docker.io/library/golang:1.27-alpine AS build

RUN apk add --no-cache git
WORKDIR /src

# go.mod pins templ as a tool, so this layer caches it with every other
# dependency and go tool runs the same version the Makefile does.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go tool templ generate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/atsume ./cmd/atsume

# Runtime: git is required because website modules are fetched at run time
# rather than baked in. They are GPL-2.0-only and are deliberately not
# distributed as part of this image.
FROM docker.io/library/alpine:3.21

RUN apk add --no-cache git ca-certificates tzdata && \
    adduser -D -u 1000 atsume

COPY --from=build /out/atsume /usr/local/bin/atsume

USER atsume
ENV ATSUME_ADDR=:8080 \
    ATSUME_DATA_DIR=/data \
    ATSUME_LIBRARY_DIR=/library

EXPOSE 8080
VOLUME ["/data", "/library"]

# No HEALTHCHECK here: podman builds OCI images by default and ignores it with
# a warning. Declare the check in whatever runs the image; /healthz answers
# 200 once the modules are loaded.

ENTRYPOINT ["/usr/local/bin/atsume"]
