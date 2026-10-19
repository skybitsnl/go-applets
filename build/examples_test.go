package build_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
)

// This example builds an applet that reverses the text of each object it
// receives, and runs it over three objects.
func Example() {
	ctx := context.Background()

	builder, err := build.NewBuilder(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer builder.Close(ctx)

	// The applet may use the types in Declarations, and its default export
	// must match Signature.
	env := build.Env{
		Declarations: `declare module "messages" {
  export interface Message { text: string }
}`,
		Signature: `import("go-applets").Applet<import("messages").Message>`,
	}

	// The applet reads each incoming message, reverses it, then yields it.
	appletSource := `
import type { Applet } from "go-applets";
import type { Message } from "messages";

const applet: Applet<Message> = function* (input) {
  for (const message of input) {
    yield { text: [...message.text].reverse().join("") };
  }
};
export default applet;
`

	// Build the applet to a WASM module.
	wasm, err := builder.Build(ctx, appletSource, env)
	if err != nil {
		log.Fatal(err)
	}

	// Create an engine to run the WASM module, including the dependencies it requires.
	engine, err := runner.NewEngine(ctx, builder.Dependencies(), runner.Limits{})
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close(ctx)

	// Load and run the applet WASM module in the engine.
	applet, err := engine.Load(ctx, wasm)
	if err != nil {
		log.Fatal(err)
	}
	defer applet.Close(ctx)

	outputs, err := applet.RunAll(ctx, nil, []json.RawMessage{
		json.RawMessage(`{"text": "hello"}`),
		json.RawMessage(`{"text": "world"}`),
		json.RawMessage(`{"text": "applets"}`),
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	for _, obj := range outputs {
		fmt.Println(string(obj))
	}
	// Output:
	// {"text":"olleh"}
	// {"text":"dlrow"}
	// {"text":"stelppa"}
}
