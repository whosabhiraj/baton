// Package protocol embeds the BATON/1 JSON Schema files, which are the master copy of the
// wire contract (see docs/GO_HUB_DESIGN.md section 2).
package protocol

import "embed"

// Schemas holds schema/*.json (JSON Schema draft 2020-12, one file per message type).
//
//go:embed schema/*.json
var Schemas embed.FS
