FROM golang:1.26.4-alpine3.22 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -o /out/integration ./src/backend/cmd/integration

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/integration /app/integration
COPY --from=build /src/manifest /app/manifest
COPY --from=build /src/web /app/web
EXPOSE 8099
ENV PORT=8099
ENTRYPOINT ["/app/integration"]
