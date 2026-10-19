module github.com/skybitsnl/go-applets

go 1.26.4

require (
	github.com/eliben/watgo v0.9.0
	github.com/evanw/esbuild v0.28.2
	github.com/skybitsnl/go-applets/appletpb v0.0.0
	github.com/skybitsnl/go-applets/javy/v9 v9.1.0
	github.com/skybitsnl/go-tsc/v7 v7.0.2
	github.com/tetratelabs/wazero v1.12.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)

replace github.com/skybitsnl/go-applets/javy/v9 => ./javy

replace github.com/skybitsnl/go-applets/appletpb => ./appletpb
