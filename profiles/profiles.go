// Package profiles embeds the shipped hardware profiles (design v3 §9.2) into
// the binary.
//
// Profiles are data, not code, and they are also the values §9.3 forbids
// hardcoding — so they must be present in every install shape: the container,
// a `go install`ed binary, and a developer checkout. Embedding removes the
// question of where they are on disk. A profile of the same name in
// $LE_DATA/config/profiles overrides the embedded one, so a measured profile
// always wins over a shipped starting point.
package profiles

import "embed"

//go:embed *.yaml
var FS embed.FS
