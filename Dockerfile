FROM golang:1.18 as builder
ENV BUILD_DIR /tmp/gfdashsync

ADD . ${BUILD_DIR}
WORKDIR ${BUILD_DIR}

RUN CGO_ENABLED=0 GOOS=linux go build -o gfdashsync main.go

FROM alpine:latest
RUN apk add --no-cache iputils ca-certificates net-snmp-tools procps &&\
    update-ca-certificates
COPY --from=builder /tmp/gfdashsync/gfdashsync /usr/bin/gfdashsync
CMD ["gfdashsync"]