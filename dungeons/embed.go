// Package dungeons carries the plan files of this build, so that tools that only read plans (the live dashboard) still have a
// plan to draw when no plans folder is given or the file is not there.
package dungeons

import "embed"

// FS holds dungeons/*.json as they were when the binary was built.
//
//go:embed *.json
var FS embed.FS
