module github.com/amberstack/bosun/cmd/bosun

go 1.22

require (
	github.com/alecthomas/chroma/v2 v2.2.0
	github.com/spf13/cobra v1.10.2
	github.com/yuin/goldmark v1.7.8
	github.com/yuin/goldmark-highlighting/v2 v2.0.0-20230729083705-37449abec8cc
)

require (
	github.com/dlclark/regexp2 v1.7.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
)

// The core module is developed in-tree; resolve it locally (the repo go.work
// also ties them together for editor/workspace builds).
replace github.com/amberstack/bosun => ../..
