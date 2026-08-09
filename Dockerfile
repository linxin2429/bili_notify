# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS ui

WORKDIR /src/web/ui
COPY web/ui/package.json web/ui/package-lock.json ./
RUN npm ci
COPY web/ui/ ./
COPY web/testdata/ /src/web/testdata/
RUN npm run build

FROM golang:1.26.5-alpine AS build

WORKDIR /src
ARG GOPROXY=https://mirrors.aliyun.com/goproxy,direct
ENV GOPROXY=${GOPROXY}
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN set -eu; \
    attempt=1; \
    while ! go mod download; do \
        if [ "${attempt}" -ge 4 ]; then \
            echo "go mod download failed after ${attempt} attempts" >&2; \
            exit 1; \
        fi; \
        delay=$((attempt * 5)); \
        echo "go mod download failed; retrying in ${delay}s (attempt $((attempt + 1))/4)" >&2; \
        sleep "${delay}"; \
        attempt=$((attempt + 1)); \
    done
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/linxin2429/bili_notify/cmd.version=${VERSION} -X github.com/linxin2429/bili_notify/cmd.commit=${COMMIT} -X github.com/linxin2429/bili_notify/cmd.date=${BUILD_DATE}" \
    -o /out/bili-notify . && mkdir -p /out/data /out/run/bili-notify

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/bili-notify /bili-notify
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/run/bili-notify /run/bili-notify
USER 65532:65532
EXPOSE 8443 9090
VOLUME ["/data", "/run/bili-notify"]
ENTRYPOINT ["/bili-notify"]
CMD ["serve"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/bili-notify", "healthcheck"]
