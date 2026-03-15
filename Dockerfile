FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/bin/app ./cmd/app
RUN CGO_ENABLED=0 go build -o /app/bin/migrate ./cmd/migrate

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/bin/app .
COPY --from=builder /app/bin/migrate .
COPY --from=builder /app/db ./db
EXPOSE 8080
# Run migrations then start app (avoids "no such table: ride_requests" on fresh deploy)
CMD ["sh", "-c", "./migrate -up && exec ./app"]
