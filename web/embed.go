// Package web embeds the built dashboard (web/dist). Before `npm run build:web` only .gitkeep is present.
package web

import "embed"

// Dist holds the dashboard build output.
//
//go:embed all:dist
var Dist embed.FS
