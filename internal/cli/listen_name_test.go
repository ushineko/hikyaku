package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultNameIsTheProject(t *testing.T) {
	assert.Equal(t, "hikyaku", defaultName("hikyaku · main · box"))
	assert.Equal(t, "my-project", defaultName("My Project · main"))
	assert.Equal(t, "notes", defaultName("notes"))
	assert.Empty(t, defaultName(""))
	assert.Len(t, defaultName(string(make([]byte, 60))+" · main"), 40)
}
