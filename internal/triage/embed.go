package triage

import "embed"

// StaticFS embeds the static assets served at /triage/static/.
// It includes pico.min.css, htmx.min.js, and triage.css.
//
//go:embed static
var StaticFS embed.FS
