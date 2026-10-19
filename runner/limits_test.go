package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/skybitsnl/go-applets/runner"
)

func TestMemoryLimit(t *testing.T) {
	ctx := context.Background()

	// Allow 4 MiB, which fits the application comfortably currently. Javy 9.1.0 requires about 1 MiB
	// itself so this even allows for some growth for Javy itself.
	small, err := runner.NewEngine(ctx, builder.Dependencies(), runner.Limits{}.WithMemoryMiB(4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { small.Close(ctx) })

	// The applet needs about 8 MiB: more than the 4 MiB limit, less than the
	// default 64 MiB.
	source := `export default function* () {
  const s = "x".repeat(8 << 20);
  yield { length: s.length };
}`

	// On the default engine, it should succeed.
	if _, err := loadInEngine(t, engine, source).RunAll(ctx, nil, nil, nil); err != nil {
		t.Errorf("with the default limit: got %v, want success", err)
	}

	// On our memory limited engine, it should fail.
	if _, err := loadInEngine(t, small, source).RunAll(ctx, nil, nil, nil); !errors.Is(err, runner.ErrMemory) {
		t.Errorf("with a memory limit: got %v, want ErrMemory", err)
	}
}
