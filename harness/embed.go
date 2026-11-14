// Package harnessassets は配置する設定ファイルを埋め込む。
package harnessassets

import "embed"

//go:embed agent-harness.config.toml
var CodexProfile []byte

// PreviousCodexProfiles は過去に配った権限プロファイルである。
// これと一致する導入済みのファイルは利用者が手を加えていないので、setup が新しい版に置き換える。
//
//go:embed previous/*.toml
var PreviousCodexProfiles embed.FS
