FROM ghcr.io/concimatteo/canzoniere-latex-2026:latest

RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1

WORKDIR /app

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY main.go .
COPY template/ ./template/
COPY mailtemplates/ ./mailtemplates/
RUN go build -mod=vendor -ldflags="-s -w" -o canzoniere-api .
COPY songs.db .
RUN luaotfload-tool --update --force
ENV TEMPLATE_PATH=/app/template
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["./canzoniere-api"]
