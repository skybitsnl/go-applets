// Command appletor runs an applet over files, and overwrites each file with
// what the applet outputs for it. See tac.ts for an example applet.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
)

// File is the object appletor sends to the applet for each file, and expects
// back from it. file.d.ts declares it for the applet.
type File struct {
	Name     string `json:"name"`
	Contents string `json:"contents"`
}

//go:embed file.d.ts
var declarations string

const signature = `import("go-applets").Applet<import("file").File>`

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s script.ts file...\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Runs script.ts over each file, and overwrites it with the output. See tac.ts for an example.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	applet, closeApplet, err := load(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	err = run(ctx, applet, flag.Args()[1:])
	closeApplet()
	if err != nil {
		log.Fatal(err)
	}
}

// load builds the applet in path, and loads it into a new Engine. The returned
// function closes both.
func load(ctx context.Context, path string) (*runner.Applet, func(), error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	builder, err := build.NewBuilder(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer builder.Close(ctx)

	wasm, err := builder.Build(ctx, string(source), build.Env{Declarations: declarations, Signature: signature})
	if err != nil {
		return nil, nil, err
	}
	engine, err := runner.NewEngine(ctx, builder.Dependencies(), runner.Limits{})
	if err != nil {
		return nil, nil, err
	}
	applet, err := engine.Load(ctx, wasm)
	if err != nil {
		engine.Close(ctx)
		return nil, nil, err
	}
	return applet, func() {
		applet.Close(ctx)
		engine.Close(ctx)
	}, nil
}

// run streams the files through the applet, one at a time, and overwrites each
// file the applet outputs. The applet may only write files it received.
func run(ctx context.Context, applet *runner.Applet, paths []string) error {
	received := make(map[string]bool, len(paths))
	for _, path := range paths {
		received[path] = true
	}
	input := func(yield func(json.RawMessage, error) bool) {
		for _, path := range paths {
			obj, err := readFile(path)
			if !yield(obj, err) || err != nil {
				return
			}
		}
	}
	output := func(obj json.RawMessage) error {
		var file File
		if err := json.Unmarshal(obj, &file); err != nil {
			return fmt.Errorf("applet output is not a file: %w", err)
		}
		if !received[file.Name] {
			return fmt.Errorf("applet output file %q, which it did not receive", file.Name)
		}
		log.Printf("Writing %s", file.Name)
		return os.WriteFile(file.Name, []byte(file.Contents), 0o644)
	}
	return applet.Run(ctx, nil, input, output, func(line string) { log.Print(line) })
}

// readFile returns the file at path as a File object.
func readFile(path string) (json.RawMessage, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(File{Name: path, Contents: string(contents)})
}
