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
├── github.com/google/licenseclassifier@v0.0.0-20210722185704-3043a050f148 Apache-2.0
│   ├── github.com/google/go-cmp@v0.2.0 BSD-3-Clause
│   ├── github.com/sergi/go-diff@v1.0.0 MIT
│   └── github.com/stretchr/testify@v1.3.0 MIT
├── github.com/sirupsen/logrus@v1.8.1 MIT
│   ├── github.com/davecgh/go-spew@v1.1.1 ISC
│   ├── github.com/pmezard/go-difflib@v1.0.0 BSD-3-Clause
│   ├── github.com/stretchr/testify@v1.2.2 MIT
│   └── golang.org/x/sys@v0.0.0-20191026070338-33540a1f6037 BSD-3-Clause
├── github.com/spf13/cobra@v1.4.0 Apache-2.0
│   ├── github.com/cpuguy83/go-md2man/v2@v2.0.1 MIT
│   ├── github.com/inconshreveable/mousetrap@v1.0.0 Apache-2.0
│   ├── github.com/spf13/pflag@v1.0.5 BSD-3-Clause
│   └── gopkg.in/yaml.v2@v2.4.0 Apache-2.0
├── github.com/xlab/treeprint@v1.1.0 MIT
│   └── github.com/stretchr/testify@v1.7.0 MIT
├── golang.org/x/mod@v0.5.1 BSD-3-Clause
│   ├── golang.org/x/crypto@v0.0.0-20191011191535-87dc89f01550 BSD-3-Clause
│   ├── golang.org/x/tools@v0.0.0-20191119224855-298f0cb1881e BSD-3-Clause
│   └── golang.org/x/xerrors@v0.0.0-20191011141410-1b5146add898 BSD-3-Clause
├── github.com/inconshreveable/mousetrap@v1.0.0 Apache-2.0
├── github.com/sergi/go-diff@v1.2.0 MIT
│   ├── github.com/davecgh/go-spew@v1.1.1 ISC
│   ├── github.com/kr/pretty@v0.1.0 MIT
│   ├── github.com/stretchr/testify@v1.4.0 MIT
│   ├── gopkg.in/check.v1@v1.0.0-20190902080502-41f04d3bba15 BSD-2-Clause
│   └── gopkg.in/yaml.v2@v2.2.4 Apache-2.0
├── github.com/spf13/pflag@v1.0.5 BSD-3-Clause
├── golang.org/x/sys@v0.0.0-20191026070338-33540a1f6037 BSD-3-Clause
└── golang.org/x/xerrors@v0.0.0-20191204190536-9bdfabe68543 BSD-3-Clause
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