module github.com/akynte/local-engineer

go 1.26.0

// Minimum patch release, not a language requirement: the standard library in
// 1.26.0 through 1.26.5 carries advisories that govulncheck reports against
// this code (net/http, crypto/tls, crypto/x509, net/url, encoding/asn1), and
// this names the current patch rather than the first one that cleared them.
// Building with an older toolchain reintroduces them.
toolchain go1.26.8

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/hashicorp/hcl/v2 v2.24.0
	github.com/landlock-lsm/go-landlock v0.10.1
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	github.com/spf13/cobra v1.10.2
	golang.org/x/tools v0.50.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.58.0
)

require (
	github.com/agext/levenshtein v1.2.1 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/scip-code/scip/bindings/go/scip v0.9.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sourcegraph/beaut v0.0.0-20240611013027-627e4c25335a // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tree-sitter/go-tree-sitter v0.25.0 // indirect
	github.com/tree-sitter/tree-sitter-go v0.25.0 // indirect
	github.com/tree-sitter/tree-sitter-rust v0.24.2 // indirect
	github.com/tree-sitter/tree-sitter-typescript v0.23.2 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/zclconf/go-cty v1.16.3 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.77 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
