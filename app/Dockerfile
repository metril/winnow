# winnow api: builds the React SPA, embeds it into the Go API binary.
FROM node:20-bookworm-slim AS fe
WORKDIR /fe
COPY app/frontend/package.json app/frontend/package-lock.json* ./
RUN npm install
COPY app/frontend/ ./
RUN npm run build

FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the placeholder SPA with the real built bundle, then embed it.
COPY --from=fe /fe/dist ./cmd/api/dist
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/api /api
EXPOSE 8000
ENTRYPOINT ["/api"]
