// Package workflows embeds the workflow template files (one JSON file each).
package workflows

import "embed"

//go:embed *.json
var FS embed.FS
