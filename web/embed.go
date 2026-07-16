package webassets

import "embed"

// Dist is replaced by the Vite production build before the control-plane binary is compiled.
//
//go:embed all:dist
var Dist embed.FS
