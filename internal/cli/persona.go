package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/persona"
)

// personaPath is the persona file the settings name, or SOUL.md beside them
// (spec 008, R1).
func (e *Env) personaPath(s config.Settings) string {
	dir := filepath.Dir(e.SettingsPath)
	switch {
	case s.Persona == "":
		return filepath.Join(dir, "SOUL.md")
	case filepath.IsAbs(s.Persona):
		return s.Persona
	}
	return filepath.Join(dir, s.Persona)
}

func personaCmd(env *Env) *cobra.Command {
	var initFile bool
	c := &cobra.Command{
		Use:   "persona",
		Short: "Say which persona the bot uses, or write the default to edit",
		Long: "The persona file (SOUL.md beside the settings, or the file `persona` names) gives the bot a name, " +
			"the voice the answerer writes in, and the lists its notes are varied from. Without one, the built-in " +
			"default applies. --init writes the default there to edit; the service picks up edits without a restart.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := env.settings()
			if err != nil {
				return err
			}
			path := env.personaPath(s)
			if initFile {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return fail(ExitError, "%v", err)
				}
				if err := persona.Init(path); err != nil {
					return fail(ExitError, "%v", err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "wrote the default persona to %s\n", path)
				return nil
			}
			l := &persona.Loader{Path: path}
			p := l.Get()
			name := p.Name
			if name == "" {
				name = "none (the answerer signs as \"answerer\")"
			}
			_, _ = fmt.Fprintf(env.Stdout, "persona: %s\nname: %s\nchances: emoji %s, aside %s\n",
				l.InEffect(), name, odds(p.EmojiOdds), odds(p.AsideOdds))
			return nil
		},
	}
	c.Flags().BoolVar(&initFile, "init", false, "write the default persona file to edit; never overwrites one")
	return c
}

func odds(n int) string {
	if n <= 0 {
		return "never"
	}
	return fmt.Sprintf("1 in %d", n)
}
