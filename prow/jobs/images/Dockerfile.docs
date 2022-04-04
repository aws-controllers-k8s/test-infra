FROM alpine:3.13.6

WORKDIR /docs
COPY build-docs.sh /docs/build-docs.sh

RUN apk add -y --no-cache \
    bash \
    gcc \
    libc-dev \
    libc6-compat \
    git \
    openssl-dev \
    libffi-dev \
    make && chmod +x /docs/build-docs.sh

RUN apk add -y --no-cache python3 python3-dev py3-pip && ln -sf python3 /usr/bin/python
RUN apk add -y --no-cache --update npm

ENTRYPOINT ["./build-docs.sh"]