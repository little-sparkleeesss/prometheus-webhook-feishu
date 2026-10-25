FROM docker.io/library/golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder

WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/prometheus-webhook-feishu .

FROM scratch

# The pinned builder image already ships ca-certificates; copying it avoids a
# floating apk install during the build.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/prometheus-webhook-feishu /prometheus-webhook-feishu

USER 65534:65534
EXPOSE 5000
ENTRYPOINT ["/prometheus-webhook-feishu"]
