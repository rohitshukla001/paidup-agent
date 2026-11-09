# Stage 1: build the Angular app into /app/web.
FROM node:22-alpine AS web
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npx ng build

# Stage 2: build the Go server.
FROM golang:1.24-alpine AS server
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Stage 3: a small runtime image with no shell, running as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=server /out/server /app/server
COPY --from=web /app/web /app/web
ENV WEB_DIR=/app/web PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app/server"]
