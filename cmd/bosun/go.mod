module github.com/amberstack/bosun/cmd/bosun

go 1.22

require (
	github.com/spf13/cobra v1.10.2
	github.com/yuin/goldmark v1.7.8
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

// The core module is developed in-tree; resolve it locally (the repo go.work
// also ties them together for editor/workspace builds).
replace github.com/amberstack/bosun => ../..
