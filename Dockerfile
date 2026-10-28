# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/chat-gateway ./cmd/gateway

FROM node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g opencode-ai@1.18.35 \
 && npm cache clean --force
COPY --from=build /out/chat-gateway /usr/local/bin/chat-gateway
WORKDIR /workspace
ENV HOME=/home/node
USER node
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/chat-gateway"]
