FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/esxi-mover ./cmd/esxi-mover

FROM scratch
COPY --from=build /out/esxi-mover /esxi-mover
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/esxi-mover"]
