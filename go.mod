module github.com/skybitsnl/go-applets

go 1.26.4

require (
	github.com/eliben/watgo v0.9.0
	github.com/evanw/esbuild v0.28.2
	github.com/skybitsnl/go-applets/javy/v9 v9.1.0
	github.com/skybitsnl/go-tsc/v7 v7.0.2
	github.com/tetratelabs/wazero v1.12.0
)

require golang.org/x/sys v0.44.0 // indirect

replace github.com/skybitsnl/go-applets/javy/v9 => ./javy
