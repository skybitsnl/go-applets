all: applets.pb.go

.PHONY: all

applets.pb.go: applets.proto
	./node_modules/.bin/grpc_tools_node_protoc \
		--go_opt=paths=source_relative \
		--go_out=. \
		--go-grpc_opt=paths=source_relative \
		--go-grpc_opt=require_unimplemented_servers=false \
		--go-grpc_out=. \
		applets.proto

node_modules: yarn.lock
	yarn install --immutable
	touch node_modules

