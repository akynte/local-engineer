// Command storescope runs the storescope analyzer as a standalone vet tool.
//
//	go run ./tools/analyzers/storescope/cmd/storescope ./...
//
// It is wired into `make check` and the CI lint job so that a violation of
// design v3 §2.3 fails the build rather than being caught in review.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/akynte/local-engineer/tools/analyzers/storescope"
)

func main() { singlechecker.Main(storescope.Analyzer) }
