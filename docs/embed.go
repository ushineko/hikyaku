/*
Package docs carries the Slack app manifest into the binary, so the setup
(spec 002) builds its app-creation link from the file the README documents.
*/
package docs

import _ "embed"

// Manifest is docs/slack-app-manifest.yaml.
//
//go:embed slack-app-manifest.yaml
var Manifest []byte
