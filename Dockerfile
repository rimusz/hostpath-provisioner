FROM golang:1.13-alpine AS builder
MAINTAINER "<rmocius@gmail..com>"

ARG srcpath="/build/hostpath-provisioner"

RUN apk --no-cache add git && \
    mkdir -p "$srcpath"

ADD . "$srcpath"

RUN cd "$srcpath" && \
    GO111MODULE=on GOPROXY=https://gocenter.io \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -ldflags '-extldflags "-static"' -o /hostpath-provisioner

FROM scratch

COPY --from=builder /hostpath-provisioner /hostpath-provisioner

CMD ["/hostpath-provisioner"]
