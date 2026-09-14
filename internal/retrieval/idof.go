package retrieval

import "github.com/akynte/local-engineer/internal/workspace"

// idOf is a tiny conversion used by the fuzz targets; keeping it in non-test
// code avoids an import cycle in the test package.
func idOf(s string) workspace.ID { return workspace.ID(s) }
