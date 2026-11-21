/*
Package persona reads the bot's persona (spec 008): its name, the voice
the answerer writes in, and the lists the service's notes are varied from.

The file is the owner's, like the settings. What it adds to Slack is
neutralized by the caller, and the voice is fenced in the answerer's prompt
after its rules; nothing here can change what a run may do.
*/
package persona

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxSize is the largest persona file read.
const MaxSize = 16 << 10

// maxName is the longest name used.
const maxName = 40

//go:embed default.md
var defaultText string

// Persona is the bot's persona.
type Persona struct {
	// Name is the bot's name; empty for none.
	Name string
	// Voice describes the tone of the answerer's answers.
	Voice string
	// Started are the notes a run starts with.
	Started []string
	// Emoji and Asides are added to notes now and then.
	Emoji, Asides []string
	// References are where the answerer's references come from; one is
	// picked at random for each answer, since a model left to choose keeps
	// choosing the same.
	References []string
	// EmojiOdds and AsideOdds are N in "1 in N"; 0 is never.
	EmojiOdds, AsideOdds int
}

// DefaultText is the default persona file, as `persona --init` writes it.
func DefaultText() string { return defaultText }

// Default is the built-in persona.
func Default() Persona {
	p, _ := parse(defaultText, Persona{})
	return p
}

var (
	comment = regexp.MustCompile(`(?s)<!--.*?-->`)
	odds    = regexp.MustCompile(`^(emoji|aside)\s*:\s*(?:1\s+in\s+(\d+)|(never))$`)
)

// Parse reads a persona file. A section it lacks is taken from the
// default; a section it does not know is ignored.
func Parse(text string) Persona {
	p, _ := parse(text, Default())
	return p
}

// parse fills base with what text's sections say, and reports which it
// found.
func parse(text string, base Persona) (Persona, map[string]bool) {
	p := base
	found := map[string]bool{}
	sections := map[string][]string{}
	var cur string
	for _, line := range strings.Split(comment.ReplaceAllString(text, ""), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			cur = strings.ToLower(strings.TrimSpace(h))
			found[cur] = true
			sections[cur] = nil
			continue
		}
		if cur != "" {
			sections[cur] = append(sections[cur], line)
		}
	}
	if found["name"] {
		p.Name = firstLine(sections["name"])
		if r := []rune(p.Name); len(r) > maxName {
			p.Name = string(r[:maxName])
		}
	}
	if found["voice"] {
		p.Voice = strings.TrimSpace(strings.Join(sections["voice"], "\n"))
	}
	for key, dst := range map[string]*[]string{
		"started": &p.Started, "emoji": &p.Emoji, "asides": &p.Asides, "references": &p.References,
	} {
		if found[key] {
			if items := list(sections[key]); len(items) > 0 {
				*dst = items
			}
		}
	}
	if found["chances"] {
		for _, item := range list(sections["chances"]) {
			m := odds.FindStringSubmatch(strings.ToLower(item))
			if m == nil {
				continue
			}
			n := 0
			if m[3] == "" {
				n, _ = strconv.Atoi(m[2])
			}
			if m[1] == "emoji" {
				p.EmojiOdds = n
			} else {
				p.AsideOdds = n
			}
		}
	}
	return p, found
}

func firstLine(lines []string) string {
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func list(lines []string) []string {
	var out []string
	for _, l := range lines {
		if item, ok := strings.CutPrefix(strings.TrimSpace(l), "- "); ok {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

// StartedNote picks the note a run starts with.
func (p Persona) StartedNote() string {
	if len(p.Started) == 0 {
		return "On it."
	}
	return p.Started[rand.IntN(len(p.Started))] //nolint:gosec // a choice of words, not a secret
}

// VoiceForAnswer is the voice for one answer: Voice, and, when the persona
// lists References, one of them picked at random to draw any reference in
// this answer from.
func (p Persona) VoiceForAnswer() string {
	if len(p.References) == 0 || strings.TrimSpace(p.Voice) == "" {
		return p.Voice
	}
	pick := p.References[rand.IntN(len(p.References))] //nolint:gosec // a choice of words, not a secret
	return p.Voice + "\n\nIf you make a reference in this answer, draw it from: " + pick + "."
}

// Flavour adds, by the persona's odds, an emoji at the end of a note and an
// aside on a line of its own. The note's own words come first, unchanged.
func (p Persona) Flavour(note string) string {
	if chance(p.EmojiOdds) && len(p.Emoji) > 0 {
		note += " " + p.Emoji[rand.IntN(len(p.Emoji))] //nolint:gosec // as above
	}
	if chance(p.AsideOdds) && len(p.Asides) > 0 {
		note += "\n_" + p.Asides[rand.IntN(len(p.Asides))] + "_" //nolint:gosec // as above
	}
	return note
}

func chance(n int) bool { return n > 0 && rand.IntN(n) == 0 } //nolint:gosec // as above

// Loader reads the persona file when it is used, and again when it has
// changed. With no file, or one it cannot use, the default applies.
type Loader struct {
	Path string
	Log  *slog.Logger

	mu      sync.Mutex
	modTime time.Time
	size    int64
	cached  *Persona
}

// Get is the persona in effect.
func (l *Loader) Get() Persona {
	if l == nil {
		return Default()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fi, err := os.Stat(l.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		l.cached = nil
		return Default()
	case err != nil:
		l.warn("read the persona file", err)
		return Default()
	case l.cached != nil && fi.ModTime().Equal(l.modTime) && fi.Size() == l.size:
		return *l.cached
	case fi.Size() > MaxSize:
		l.warn("the persona file is over 16 KB; using the default", nil)
		return Default()
	}
	b, err := os.ReadFile(l.Path)
	if err != nil {
		l.warn("read the persona file", err)
		return Default()
	}
	p := Parse(string(b))
	l.cached, l.modTime, l.size = &p, fi.ModTime(), fi.Size()
	return p
}

// InEffect says which file is used: its path, or that the default is.
func (l *Loader) InEffect() string {
	fi, err := os.Stat(l.Path)
	switch {
	case err != nil:
		return "the built-in default (no " + l.Path + ")"
	case fi.Size() > MaxSize:
		return "the built-in default (" + l.Path + " is over 16 KB)"
	}
	return l.Path
}

func (l *Loader) warn(msg string, err error) {
	if l.Log == nil {
		return
	}
	if err != nil {
		l.Log.Warn(msg, "path", l.Path, "err", err)
		return
	}
	l.Log.Warn(msg, "path", l.Path)
}

// Init writes the default persona to path, refusing to overwrite a file.
func Init(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the owner's own config path
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s exists; edit it, or remove it first", path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.WriteString(defaultText); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
