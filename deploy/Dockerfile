# Build stage: templ generation happens here so the image needs no committed
# generated code.
FROM docker.io/library/golang:1.27-alpine AS build

RUN apk add --no-cache git
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/a-h/templ/cmd/templ@v0.3.906

COPY . .
RUN templ generate && \
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

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/atsume"]
