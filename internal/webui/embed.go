package webui

import "embed"

//go:embed index.html app.js style.css icon.svg
var FS embed.FS
