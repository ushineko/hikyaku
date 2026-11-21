package persona_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/persona"
)

const full = `# Mine

## Name

nykbot-α

## Voice

Terse. Puns allowed.

## Started

- Hmm.
- Right.

## Emoji

- 🦑

## Asides

- Beep.

## Chances

- emoji: 1 in 1
- aside: never

## Favourite colour

- teal
`

// AC1: every section is read; a missing one is the default's; an unknown
// one is ignored.
func TestAPersonaFileIsRead(t *testing.T) {
	p := persona.Parse(full)
	assert.Equal(t, persona.Persona{
		Name: "nykbot-α", Voice: "Terse. Puns allowed.", Started: []string{"Hmm.", "Right."},
		Emoji: []string{"🦑"}, Asides: []string{"Beep."}, EmojiOdds: 1, AsideOdds: 0,
	}, p)

	d := persona.Default()
	assert.Empty(t, d.Name, "the default has no name: the owner's app name is theirs")
	assert.Contains(t, d.Voice, "dry sense of humour")
	assert.Contains(t, d.Emoji, "🐙")
	assert.Equal(t, 3, d.EmojiOdds)
	assert.Equal(t, 8, d.AsideOdds)

	some := persona.Parse("## Name\n\nbob\n\n## Chances\n\n- aside: 1 in 2\n")
	assert.Equal(t, "bob", some.Name)
	assert.Equal(t, d.Voice, some.Voice)
	assert.Equal(t, d.Started, some.Started)
	assert.Equal(t, d.EmojiOdds, some.EmojiOdds, "a chance not given keeps the default's")
	assert.Equal(t, 2, some.AsideOdds)

	assert.Equal(t, strings.Repeat("x", 40), persona.Parse("## Name\n"+strings.Repeat("x", 99)).Name)
}

// AC4: with every chance, a note gets an emoji at its end and an aside on
// its own line, its own words first and unchanged; with none, nothing.
func TestFlavourIsAddedNeverSubstituted(t *testing.T) {
	d := persona.Default()
	always := d
	always.EmojiOdds, always.AsideOdds = 1, 1
	note := "Queued: 2 ahead of it."
	for range 50 {
		got := always.Flavour(note)
		first, aside, ok := strings.Cut(got, "\n")
		require.True(t, ok, got)
		emoji, found := strings.CutPrefix(first, note+" ")
		require.True(t, found, got)
		assert.Contains(t, d.Emoji, emoji)
		assert.True(t, slices.Contains(d.Asides, strings.Trim(aside, "_")), aside)
	}
	never := d
	never.EmojiOdds, never.AsideOdds = 0, 0
	assert.Equal(t, note, never.Flavour(note))
}

// AC5: the started note comes from the list.
func TestTheStartedNoteComesFromTheList(t *testing.T) {
	p := persona.Parse(full)
	for range 20 {
		assert.Contains(t, p.Started, p.StartedNote())
	}
}

// AC1, AC2: no file, or one too big, is the default, logged; an edit is
// used next time without a restart.
func TestTheLoaderFollowsTheFile(t *testing.T) {
	var log bytes.Buffer
	path := filepath.Join(t.TempDir(), "SOUL.md")
	l := &persona.Loader{Path: path, Log: slog.New(slog.NewTextHandler(&log, nil))}
	assert.Equal(t, persona.Default(), l.Get())

	require.NoError(t, os.WriteFile(path, []byte("## Name\n\nfirst\n"), 0o600))
	assert.Equal(t, "first", l.Get().Name)
	require.NoError(t, os.WriteFile(path, []byte("## Name\n\nsecond one\n"), 0o600))
	later := time.Now().Add(time.Second)
	require.NoError(t, os.Chtimes(path, later, later))
	assert.Equal(t, "second one", l.Get().Name, "an edit is picked up")

	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), persona.MaxSize+1), 0o600))
	assert.Equal(t, persona.Default(), l.Get())
	assert.Contains(t, log.String(), "over 16 KB")
}

// The default file is what Default reads, and Init never overwrites.
func TestInitWritesTheDefaultOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL.md")
	require.NoError(t, persona.Init(path))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, persona.DefaultText(), string(b))
	assert.Equal(t, persona.Default(), persona.Parse(string(b)))
	assert.ErrorContains(t, persona.Init(path), "exists")
}
