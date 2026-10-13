FROM golang:1.26.4-alpine3.22 AS build
ARG TARGETARCH=amd64
ARG GO_VERSION=1.26.9
RUN apk add --no-cache curl ca-certificates \
	&& rm -rf /usr/local/go \
	&& curl --fail --location --silent --show-error "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
		| tar -C /usr/local -xz \
	&& /usr/local/go/bin/go version
ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -o /out/integration ./src/backend/cmd/integration

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/integration /app/integration
COPY --from=build /src/manifest /app/manifest
COPY --from=build /src/web /app/web
EXPOSE 8099
ENV PORT=8099
ENTRYPOINT ["/app/integration"]
