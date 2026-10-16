// Package pb contains the generated protobuf / gRPC code of the HA peer
// protocol. Regenerate with `go generate ./internal/ha/pb` (needs protoc).
package pb

//go:generate protoc -I . --plugin=protoc-gen-go=../../../bin/protoc-gen-go --plugin=protoc-gen-go-grpc=../../../bin/protoc-gen-go-grpc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ha.proto
