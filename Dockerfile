FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY main.go .
RUN CGO_ENABLED=1 go build -mod=vendor -ldflags="-s -w" -o /canzoniere-api .

FROM ghcr.io/concimatteo/canzoniere-latex-2026:latest
WORKDIR /app
COPY --from=build /canzoniere-api ./canzoniere-api
COPY template/ ./template/
COPY mailtemplates/ ./mailtemplates/
COPY songs.db .
RUN luaotfload-tool --update --force
ENV TEMPLATE_PATH=/app/template
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["./canzoniere-api"]
