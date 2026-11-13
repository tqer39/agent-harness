// Package harnessassets は配置する設定ファイルを埋め込む。
package harnessassets

import _ "embed"

//go:embed agent-harness.config.toml
var CodexProfile []byte
