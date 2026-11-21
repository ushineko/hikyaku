package answerer_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ushineko/hikyaku/internal/answerer"
)

// Fake credentials are put together at run time, so no secret scanner
// takes this file for one.
func fake(prefix string, n int) string {
	return prefix + strings.Repeat("Q7", n/2) + strings.Repeat("x", n%2)
}

// R10: no credential leaves in an answer, whatever its shape; the name
// next to it stays, so the answer still says what was there.
func TestAnAnswerCarriesNoSecret(t *testing.T) {
	key := "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA\nQ7Q7Q7\n-----END OPENSSH " + "PRIVATE KEY-----"
	secrets := map[string]string{
		"private key":   key,
		"cut-off key":   "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIEow",
		"aws":           fake("AKIA", 16),
		"github":        fake("ghp_", 36),
		"github pat":    fake("github_pat_", 60),
		"gitlab":        fake("glpat-", 20),
		"slack":         fake("xoxb-", 30),
		"slack app":     fake("xapp-", 30),
		"atlassian":     fake("ATATT", 40),
		"anthropic":     fake("sk-ant-", 40),
		"google":        fake("AIza", 35),
		"jwt":           "eyJ" + fake("", 20) + "." + fake("", 20) + "." + fake("", 20),
		"url password":  "https://deploy:" + fake("", 12) + "@gitlab.invented/x.git",
		"env line":      "DATABASE_PASSWORD=" + fake("", 14),
		"yaml":          "api_key: \"" + fake("", 18) + "\"",
		"client secret": "client_secret = " + fake("", 24),
	}
	for name, secret := range secrets {
		t.Run(name, func(t *testing.T) {
			got, n := answerer.Scrub("Found this in the file:\n```\n" + secret + "\n```\nThat is all.")
			assert.Equal(t, 1, n.Secrets, got)
			assert.Contains(t, got, answerer.Redacted)
			assert.Contains(t, got, "That is all.")
			assert.NotContains(t, got, "Q7Q7Q7", "the secret's body is gone")
			assert.NotContains(t, got, "MIIEow")
		})
	}

	got, _ := answerer.Scrub("DATABASE_PASSWORD=" + fake("", 14) + " and https://deploy:" + fake("", 12) + "@host/x")
	assert.Equal(t, "DATABASE_PASSWORD=[redacted] and https://deploy:[redacted]@host/x", got)
}

// R10: email addresses and phone numbers are masked; names, repository
// addresses and ordinary numbers are not.
func TestAnAnswerCarriesNoContactDetails(t *testing.T) {
	got, n := answerer.Scrub("Ask Jane Doe <jane.doe@example.com> or call +1 415-555-0134 or (415) 555-0135. " +
		"Clone git@gitlab.invented:team/agent.git. Release 4.12.0 shipped 2026-11-20, MR !1234, pipeline 9876543210, " +
		"build 123-456, ticket AP-16334.")
	assert.Equal(t, "Ask Jane Doe <[email]> or call [phone] or [phone]. "+
		"Clone git@gitlab.invented:team/agent.git. Release 4.12.0 shipped 2026-11-20, MR !1234, pipeline 9876543210, "+
		"build 123-456, ticket AP-16334.", got)
	assert.Equal(t, answerer.Scrubbed{Emails: 1, Phones: 2}, n)
}

// An answer about secrets that holds none is left alone.
func TestAnAnswerWithoutSecretsIsUnchanged(t *testing.T) {
	for _, s := range []string{
		"The token is read from `$GITLAB_TOKEN`; set password: ${DB_PASSWORD} in the env file.",
		"*Release 4.12.0* is out: <https://gitlab.invented/team/agent/-/releases/4.12.0|release page>. CI passed.",
		"The `.env` file exists in `platform-backend` but I can't read it.",
		"Author: Jane Doe, 3 days ago. Tokens used: 12.",
	} {
		got, n := answerer.Scrub(s)
		assert.Equal(t, s, got)
		assert.Zero(t, n)
	}
}
