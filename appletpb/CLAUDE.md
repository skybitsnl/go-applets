# gRPC proto files

This package describes the interface for appletd.

### Regenerating proto code

In `appletpb/`:

```sh
yarn install --immutable
make
```

The Makefile runs `grpc_tools_node_protoc` to generate the Go files. After
changing `.proto` files, always run `make`. Keep proto changes
backwards-compatible where possible.

`client.go` and `client_test.go` are written by hand: helpers for clients, and
`Error` methods on generated messages. Check them after changing the `.proto`.
