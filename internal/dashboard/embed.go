package dashboard

import "embed"

// staticFiles contains the built Svelte dashboard assets.
//
//go:embed all:static
var staticFiles embed.FS
