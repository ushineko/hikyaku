package docs_test

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/ushineko/hikyaku/docs"
)

type manifest struct {
	OAuth struct {
		Scopes struct {
			Bot []string `yaml:"bot"`
		} `yaml:"scopes"`
	} `yaml:"oauth_config"`
	Settings struct {
		Events struct {
			Bot []string `yaml:"bot_events"`
		} `yaml:"event_subscriptions"`
		SocketMode bool `yaml:"socket_mode_enabled"`
	} `yaml:"settings"`
}

// R11 and AC10: the manifest asks for Socket Mode, and every scope it asks
// for is in the README's table with a reason, and nothing more.
func TestTheManifestAndTheReadmeAgree(t *testing.T) {
	var m manifest
	require.NoError(t, yaml.Unmarshal(docs.Manifest, &m))
	assert.True(t, m.Settings.SocketMode)
	assert.Subset(t, m.Settings.Events.Bot, []string{"message.im"})

	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)
	row := regexp.MustCompile("(?m)^\\| `([a-z.:]+)` \\| ([^|]+) \\|$")
	var documented []string
	for _, r := range row.FindAllStringSubmatch(string(readme), -1) {
		if r[2] == "" {
			continue
		}
		documented = append(documented, r[1])
	}
	slices.Sort(documented)
	scopes := slices.Clone(m.OAuth.Scopes.Bot)
	slices.Sort(scopes)
	assert.Equal(t, scopes, documented, "README's scope table and the manifest's bot scopes differ")
}
