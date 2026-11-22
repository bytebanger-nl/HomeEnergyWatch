# Build: docker build -t hwlocal .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hwlocal ./cmd/hwlocal

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hwlocal /hwlocal
ENV HWLOCAL_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080 8443
ENTRYPOINT ["/hwlocal"]
