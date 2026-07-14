FROM ghcr.io/concimatteo/canzoniere-base:1.5
WORKDIR /app

COPY go.mod go.sum ./
COPY vendor/ ./vendor/

COPY main.go .
COPY template/ ./template/

RUN go build -mod=vendor -ldflags="-s -w" -o canzoniere-api .
COPY songs.db .

RUN luaotfload-tool --update --force

ENV TEMPLATE_PATH=/app/template
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["./canzoniere-api"]