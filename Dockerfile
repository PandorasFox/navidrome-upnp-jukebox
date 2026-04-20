FROM node:20-alpine AS frontend-builder

ARG FRONTEND_HASH
WORKDIR /build/frontend-react
COPY ../frontend-react/package.json ../frontend-react/package-lock.json ./
RUN npm ci
COPY ../frontend-react/ .
RUN echo "Frontend hash: ${FRONTEND_HASH}" && npm run build

FROM golang:1.22-alpine AS builder

WORKDIR /build

ARG BACKEND_HASH
ARG FRONTEND_HASH

RUN echo "Backend hash: ${BACKEND_HASH}  Frontend hash: ${FRONTEND_HASH}" && apk add --no-cache gcc musl-dev

COPY go/go.mod go/go.sum ./
RUN go mod download

COPY go/ .

RUN CGO_ENABLED=1 GOOS=linux go build -o jukebox ./cmd/server

# Final stage
FROM alpine:3.19

WORKDIR /app

COPY --from=builder /build/jukebox .
COPY --from=frontend-builder /build/frontend-react/dist ./frontend

ENTRYPOINT ["./jukebox"]
