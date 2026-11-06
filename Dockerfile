FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SOURCE_REVISION=unknown
RUN if [ "$SOURCE_REVISION" != unknown ]; then \
      [ "${#SOURCE_REVISION}" = 40 ] || exit 1; \
      case "$SOURCE_REVISION" in *[!0-9a-f]*) exit 1;; esac; \
    fi && printf '{"schema_version":1,"source_commit":"%s"}\n' "$SOURCE_REVISION" > web/static/build-info.json
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=build /out/ /usr/local/bin/
COPY --from=build /src/web/static/ /usr/local/share/headcount/static/
COPY --from=build /src/internal/store/migrations/ /usr/local/share/headcount/migrations/
USER app
ENTRYPOINT ["/usr/local/bin/collector"]
