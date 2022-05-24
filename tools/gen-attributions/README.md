# gen-attributions CLI tool

`gen-attributions` parses a given go module file (`go.mod`) and generates
the attributions file for it (`ATTRIBUTIONS.md` in ACK land). 

## Prerequisities

- Go compiler

## Using gen-attributions

The easiest way to use `gen-attributions` is to run it inside the directory 
of your Go project.

```bash
gen-attributions --debug # default generated file name is ATTRIBUTIONS.md
```

By default the max depth allowed while exploring the dependency graph is 2,
you can override this value by using the `--depth` flag.

```bash
gen-attributions --depth 5 --debug
```

You can also set the output/input and the templates used to generation the
attributions file.

```bash
gen-attributions --output ATTRIBUTIONS.md --modfile go.mod\
    --attr-header-template $(HEADER_TMP)\
    --attr-block-template $(BLOCK_TMP)
```

You can also print the dependency graph using the `--show-graph` flag

```bash
gen-attributions --show-graph --depth 5

# OUTPUT
INFO[0010] gen-attributions
├── github.com/sirupsen/logrus@v1.8.1
│   ├── github.com/davecgh/go-spew@v1.1.1
│   ├── github.com/pmezard/go-difflib@v1.0.0
│   ├── github.com/stretchr/testify@v1.2.2
│   └── golang.org/x/sys@v0.0.0-20191026070338-33540a1f6037
├── github.com/spf13/cobra@v1.4.0
│   ├── github.com/cpuguy83/go-md2man/v2@v2.0.1
│   │   └── github.com/russross/blackfriday/v2@v2.1.0
│   ├── github.com/inconshreveable/mousetrap@v1.0.0
│   ├── github.com/spf13/pflag@v1.0.5
│   └── gopkg.in/yaml.v2@v2.4.0
│       └── gopkg.in/check.v1@v0.0.0-20161208181325-20d25e280405
├── github.com/xlab/treeprint@v1.1.0
│   └── github.com/stretchr/testify@v1.7.0
│       ├── github.com/davecgh/go-spew@v1.1.0
│       ├── github.com/pmezard/go-difflib@v1.0.0
│       ├── github.com/stretchr/objx@v0.1.0
│       └── gopkg.in/yaml.v3@v3.0.0-20200313102051-9f266ea9e77c
│           └── gopkg.in/check.v1@v0.0.0-20161208181325-20d25e280405
├── golang.org/x/mod@v0.5.1
│   ├── golang.org/x/crypto@v0.0.0-20191011191535-87dc89f01550
│   │   ├── golang.org/x/net@v0.0.0-20190404232315-eb5bcb51f2a3
│   │   │   ├── golang.org/x/crypto@v0.0.0-20190308221718-c2843e01d9a2
│   │   │   │   └── golang.org/x/sys@v0.0.0-20190215142949-d0b11bdaac8a
│   │   │   └── golang.org/x/text@v0.3.0
│   │   └── golang.org/x/sys@v0.0.0-20190412213103-97732733099d
│   ├── golang.org/x/tools@v0.0.0-20191119224855-298f0cb1881e
│   │   ├── golang.org/x/net@v0.0.0-20190620200207-3b0461eec859
│   │   │   ├── golang.org/x/crypto@v0.0.0-20190308221718-c2843e01d9a2
│   │   │   │   └── golang.org/x/sys@v0.0.0-20190215142949-d0b11bdaac8a
│   │   │   ├── golang.org/x/sys@v0.0.0-20190215142949-d0b11bdaac8a
│   │   │   └── golang.org/x/text@v0.3.0
│   │   ├── golang.org/x/sync@v0.0.0-20190423024810-112230192c58
│   │   └── golang.org/x/xerrors@v0.0.0-20190717185122-a985d3407aa7
│   └── golang.org/x/xerrors@v0.0.0-20191011141410-1b5146add898
├── github.com/inconshreveable/mousetrap@v1.0.0
├── github.com/spf13/pflag@v1.0.5
├── golang.org/x/sys@v0.0.0-20191026070338-33540a1f6037
└── golang.org/x/xerrors@v0.0.0-20191011141410-1b5146add898
```

## Usage

```sh
A tool to generate attributions file for Go projects

Usage:
  gen-attributions [flags]

Flags:
      --attr-block-template string    'Module block template used to generate the attribution file (default "\n{{ .TitlePrefix }} {{ .Name }}\n\n{{ .License }}\n\n{{ if .Dependencies -}}\nSubdependencies:\n{{ range $dependency := .Dependencies -}}\n* `{{ .Version.Path }}`\n{{ end }}\n{{- end }}\n")'
      --attr-header-template string   'Header template used to generate the attribution file (default "{{ .Header }}\n\n{{ if .Tree.Root.Dependencies -}}\n{{ range $dependency := .Tree.Root.Dependencies -}}\n* `{{ .Version.Path }}`\n{{ end -}}\n{{ end -}}\n")'
      --debug                         Show debug output
      --depth int                     Depth of the dependency tree to explore (default 2)
      --go-proxy-url string           Go proxy used to fetch module versions and licenses (default "http://proxy.golang.org")
  -h, --help                          help for gen-attributions
      --modfile string                Go module file path (default "go.mod")
  -o, --output string                 Output file name (default "ATTRIBUTIONS.md")
      --show-graph                    Show the dependency graph in stdout
```