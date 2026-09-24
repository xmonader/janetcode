# janetcode in a container: the terminal UI, headless -p, or --web.
#
#   docker run -it --rm --user "$(id -u):$(id -g)" \
#     -v "$PWD:/work" -e OPENAI_API_KEY ghcr.io/xmonader/janetcode
#
# --user keeps the files it writes owned by you, not by root. For --web,
# publish the port and bind every interface inside the container; that is
# off loopback, so the page asks for the token in the URL it prints:
#
#   docker run --rm -p 127.0.0.1:8787:8787 -v "$PWD:/work" -e OPENAI_API_KEY \
#     ghcr.io/xmonader/janetcode --web --web-addr 0.0.0.0:8787
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /janetcode ./cmd/janetcode

FROM debian:stable-slim
# git for diffs, undo and --proof; ripgrep and the usual tools for the
# commands the model runs.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates ripgrep less procps \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /janetcode /usr/local/bin/janetcode
# Any uid given with --user needs a writable home for config and sessions,
# and git refuses a mounted repository owned by someone else.
ENV HOME=/home/janetcode
RUN mkdir -p /home/janetcode && chmod 1777 /home/janetcode \
 && git config --system --add safe.directory '*'
WORKDIR /work
ENTRYPOINT ["janetcode"]
