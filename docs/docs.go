// Package docs は `aitodo manual` で表示するマニュアルを埋め込む。
package docs

import _ "embed"

//go:embed manual.md
var Manual string

//go:embed snippet.md
var Snippet string
