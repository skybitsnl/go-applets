package build

import (
	_ "embed"
	"fmt"

	"github.com/evanw/esbuild/pkg/api"
)

//go:embed prelude.js
var prelude string

const appletPath = "applet"

// Bundle turns the applet's TypeScript source into one self-contained
// JavaScript script, with the prelude around it. It uses the esbuild Go library
// for this. Types are stripped, not checked. The applet may not import anything
// at runtime; type-only imports are removed. It returns an InvalidSourceError
// if the source is invalid.
func Bundle(source string) (string, error) {
	res := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   prelude,
			Sourcefile: "prelude.js",
			Loader:     api.LoaderJS,
		},
		Bundle:   true,
		Write:    false,
		Format:   api.FormatIIFE,
		Platform: api.PlatformNeutral,
		Target:   api.ES2023,
		LogLevel: api.LogLevelSilent,
		Plugins:  []api.Plugin{appletPlugin(source)},
	})

	if len(res.Errors) > 0 {
		err := &InvalidSourceError{Step: StepBundle}
		for _, m := range res.Errors {
			d := SourceError{Message: m.Text}
			// esbuild names files outside the file namespace "<namespace>:<path>".
			if m.Location != nil && m.Location.File == "applet:"+appletPath {
				d.Line = m.Location.Line
				d.Column = m.Location.Column + 1
			}
			err.Errors = append(err.Errors, d)
		}
		return "", err
	}
	if len(res.OutputFiles) != 1 {
		return "", fmt.Errorf("expected 1 output file, got %d", len(res.OutputFiles))
	}
	return string(res.OutputFiles[0].Contents), nil
}

// appletPlugin is an esbuild plugin that allows loading the applet source, and
// allows no other imports.
func appletPlugin(source string) api.Plugin {
	return api.Plugin{
		Name: "applet",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.Path == appletPath && args.Importer == "prelude.js" {
					return api.OnResolveResult{Path: appletPath, Namespace: "applet"}, nil
				}
				return api.OnResolveResult{}, fmt.Errorf("applets cannot import %q", args.Path)
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "applet"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				return api.OnLoadResult{Contents: &source, Loader: api.LoaderTS}, nil
			})
		},
	}
}
