# brainapi/v1: the Brain gRPC contract

Package `brainapiv1` (`github.com/cybericebox/backend/pkg/brainapi/v1`) is the gRPC contract between a client (the platform backend) and the Brain service: `brain.proto` and the Go generated from it (`brain.pb.go`, `brain_grpc.pb.go`).

- `brain.proto` is authored by the Brain side and landed here as is. Do not edit it in this repository by hand; a change comes from the Brain side and goes through a pull request into this repository.
- The Brain imports this package, so it imports only `google.golang.org/grpc` and `google.golang.org/protobuf` and nothing else of this module (a test enforces it).
- The generated Go is committed. Regenerate it after a change of `brain.proto`:

  ```sh
  mise run gen-brainapi
  ```

  protoc, protoc-gen-go and protoc-gen-go-grpc are pinned in `mise.toml`; the well-known types (empty, timestamp) come from the includes of the pinned protoc.
- `mise run lint:brainapi` (part of `mise run lint`, which CI runs) fails when the committed Go differs from a fresh generation.
