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
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/linxin2429/bili_notify/cmd.version=${VERSION} -X github.com/linxin2429/bili_notify/cmd.commit=${COMMIT} -X github.com/linxin2429/bili_notify/cmd.date=${BUILD_DATE}" \
    -o /out/bili-notify . && mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/bili-notify /bili-notify
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8443 9090
VOLUME ["/data"]
ENTRYPOINT ["/bili-notify"]
CMD ["serve"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/bili-notify", "healthcheck"]
