# syntax=docker/dockerfile:experimental
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.13-alpine as builder

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION

ARG TARGETPLATFORM
ARG BUILDPLATFORM
RUN printf "I am running on ${BUILDPLATFORM:-linux/amd64}, building for ${TARGETPLATFORM:-linux/amd64}\n$(uname -a)\n" \
  && $(case ${TARGETPLATFORM:-linux/amd64} in \
      "linux/amd64")   echo "GOOS=linux GOARCH=amd64" > /tmp/.env                       ;; \
      "linux/arm/v6")  echo "GOOS=linux GOARCH=arm GOARM=6" > /tmp/.env                 ;; \
      "linux/arm/v7")  echo "GOOS=linux GOARCH=arm GOARM=7" > /tmp/.env                 ;; \
      "linux/arm64")   echo "GOOS=linux GOARCH=arm64" > /tmp/.env                       ;; \
      "linux/386")     echo "GOOS=linux GOARCH=386" > /tmp/.env                         ;; \
      "linux/ppc64le") echo "GOOS=linux GOARCH=ppc64le" > /tmp/.env                     ;; \
      "linux/s390x")   echo "GOOS=linux GOARCH=s390x" > /tmp/.env                       ;; \
      *)               echo "TARGETPLATFORM ${TARGETPLATFORM} not found..." && exit 1   ;; \
    esac) \
  && cat /tmp/.env
RUN env $(cat /tmp/.env | xargs) go env

RUN apk --update --no-cache add \
    build-base \
    gcc \
    git \
  && rm -rf /tmp/* /var/cache/apk/*

WORKDIR /app

ENV GO111MODULE on
ENV GOPROXY https://goproxy.io
COPY go.mod .
COPY go.sum .
RUN env $(cat /tmp/.env | xargs) go mod download
COPY . ./

ARG VERSION=dev
RUN env $(cat /tmp/.env | xargs) go build -ldflags "-w -s -X 'main.version=${VERSION}'" -v -o ddns-route53 cmd/main.go

FROM --platform=${TARGETPLATFORM:-linux/amd64} alpine:latest

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION

LABEL maintainer="CrazyMax" \
  org.opencontainers.image.created=$BUILD_DATE \
  org.opencontainers.image.url="https://github.com/crazy-max/ddns-route53" \
  org.opencontainers.image.source="https://github.com/crazy-max/ddns-route53" \
  org.opencontainers.image.version=$VERSION \
  org.opencontainers.image.revision=$VCS_REF \
  org.opencontainers.image.vendor="CrazyMax" \
  org.opencontainers.image.title="ddns-route53" \
  org.opencontainers.image.description="Dynamic DNS for Amazon Route 53‎ on a time-based schedule" \
  org.opencontainers.image.licenses="MIT"

RUN apk --update --no-cache add \
    ca-certificates \
    libressl \
    shadow \
  && addgroup -g 1000 ddns-route53 \
  && adduser -u 1000 -G ddns-route53 -s /sbin/nologin -D ddns-route53 \
  && rm -rf /tmp/* /var/cache/apk/*

COPY --from=builder /app/ddns-route53 /usr/local/bin/ddns-route53
COPY --from=builder /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/zoneinfo.zip
RUN ddns-route53 --version

USER ddns-route53

ENTRYPOINT [ "/usr/local/bin/ddns-route53" ]
CMD [ "--config", "/ddns-route53.yml" ]
