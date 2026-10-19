// Command appletor-remote runs an applet over files, and overwrites each file
// with what the applet outputs for it, like appletor. Unlike appletor, it does
// not build and run the applet itself, but has an appletd server do it, as an
// example of a client of package appletpb. Applets for appletor work here too,
// such as ../appletor/tac.ts.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/skybitsnl/go-applets/appletpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// File is the object appletor-remote sends to the applet for each file, and
// expects back from it, as in appletor. file.d.ts declares it for the applet.
type File struct {
	Name     string `json:"name"`
	Contents string `json:"contents"`
}

//go:embed file.d.ts
var declarations string

const signature = `import("go-applets").Applet<import("file").File>`

var server = flag.String("server", "localhost:2080", "the `address` of appletd")

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [-server address] script.ts file...\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Runs script.ts over each file on appletd, and overwrites it with the output.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	stream, closeConn, err := open(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	err = run(stream, flag.Args()[1:])
	closeConn()
	if err != nil {
		log.Fatal(err)
	}
}

// open sends the applet in path to the server, which builds it. The returned
// function closes the connection.
func open(ctx context.Context, path string) (*appletpb.Stream, func(), error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(*server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	stream, err := appletpb.Open(ctx, appletpb.NewAppletsClient(conn), &appletpb.Start{
		Applets:      []*appletpb.Applet{{Source: string(source), Signature: signature}},
		Declarations: declarations,
	})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return stream, func() { conn.Close() }, nil
}

// run streams the files through the applet, one at a time, and overwrites each
// file the applet outputs. The applet may only write files it received.
func run(stream *appletpb.Stream, paths []string) error {
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
	return stream.Run(input, output, func(_ int, line string) { log.Print(line) })
}

// readFile returns the file at path as a File object.
func readFile(path string) (json.RawMessage, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(File{Name: path, Contents: string(contents)})
}
