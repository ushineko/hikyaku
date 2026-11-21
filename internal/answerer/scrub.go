package answerer

import (
	"regexp"
	"strings"
)

// secretFiles are files a run may not read or search, whatever the
// allow-list says: they hold credentials, not answers. A deny rule wins
// over an allow rule in Claude Code.
var secretFiles = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*",
	".netrc", ".npmrc", ".pypirc", ".pgpass", ".htpasswd", ".git-credentials", "credentials*",
	".git/config", "*.tfstate", "*.tfstate.*", "*.tfvars", "kubeconfig*",
}

// DeniedTools are the deny rules every run gets: no Read or Grep of a
// secret file under dir.
func DeniedTools(dir string) []string {
	var out []string
	for _, tool := range []string{"Read", "Grep"} {
		for _, f := range secretFiles {
			out = append(out, tool+"("+dir+"/**/"+f+")")
		}
	}
	return out
}

// secretPatterns are shapes of credentials. A match is replaced whole,
// except where a group names what to keep: the name in `password=…`, the
// user in `https://user:…@`.
var secretPatterns = []*regexp.Regexp{
	// A key cut off before its END line runs as far as its base64 does.
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?:(?s:.*?)-----END [A-Z ]*PRIVATE KEY-----|[A-Za-z0-9+/=\s]*)`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`),
	regexp.MustCompile(`\bgl[a-z]{2,4}-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bx(?:ox[a-z]|app)-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bATATT[A-Za-z0-9_=-]{30,}`),
	regexp.MustCompile(`\bsk-(?:ant-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
}

// keepAround patterns keep their groups and replace what lies between.
var keepAround = []*regexp.Regexp{
	regexp.MustCompile(`(\b[a-z][a-z0-9+.-]*://[^\s:/@<>|]+:)[^\s@/<>|]+(@)`),
	regexp.MustCompile(`(?i)(\b[a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret)[a-z0-9_.-]*["']?\s*[:=]\s*["']?)[^\s"'` + "`" + `$<*{][^\s"'` + "`" + `]{5,}`),
}

var (
	emailPattern = regexp.MustCompile(`\b([A-Za-z0-9._%+-]+)@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`)
	phonePattern = regexp.MustCompile(`(?:\+\d{1,3}[\s.-]?)?(?:\(\d{3}\)\s?|\b\d{3}[\s.-])\d{3}[\s.-]\d{4}\b|\+\d{1,3}(?:[\s.-]\d{2,4}){2,4}\b`)
)

// Redacted is what a removed secret is replaced with.
const Redacted = "[redacted]"

// Scrubbed counts what Scrub removed.
type Scrubbed struct {
	Secrets, Emails, Phones int
}

// Scrub removes credentials, email addresses and phone numbers from an
// answer before it leaves the machine (spec 007, R10). It runs on every
// answer, whatever the run read: a secret can arrive through an allowed
// route, such as a key in a commit or a token pasted in an issue.
func Scrub(text string) (string, Scrubbed) {
	var n Scrubbed
	for _, p := range secretPatterns {
		text = p.ReplaceAllStringFunc(text, func(string) string { n.Secrets++; return Redacted })
	}
	for _, p := range keepAround {
		n.Secrets += len(p.FindAllStringIndex(text, -1))
		text = p.ReplaceAllString(text, "${1}"+Redacted+"${2}")
	}
	text = emailPattern.ReplaceAllStringFunc(text, func(m string) string {
		if strings.EqualFold(emailPattern.FindStringSubmatch(m)[1], "git") {
			return m // git@host:repo is an address of a repository, not a person
		}
		n.Emails++
		return "[email]"
	})
	text = phonePattern.ReplaceAllStringFunc(text, func(string) string { n.Phones++; return "[phone]" })
	return text, n
}

// Paths are the folders whose absolute paths an answer must not show
// (spec 009): they name the owner and the layout of their disk, and mean
// nothing to anyone else.
type Paths struct {
	// Home is the owner's home directory; Source is the folder the runs
	// read repositories from (~/git).
	Home, Source string
}

// LocalPath is what a path on the machine outside the source folder
// becomes.
const LocalPath = "[a local path]"

// localPath matches a path on the machine: under a home directory (~/,
// /home, /Users, /root) or a system folder. URL paths and API routes
// (/api/v4/…) do not start with one of these, and are left alone.
var localPath = regexp.MustCompile(`(?:^|[\s(\x60'"=:])((?:~|/(?:home|Users|root|tmp|var|etc|opt|usr|mnt|media|srv|private))/[^\s\x60'")\]>,;|]*)`)

// Generalize rewrites the machine's paths in an answer (spec 009): a path
// under Source becomes relative to it, starting with the repository's
// name; any other path on the machine becomes LocalPath, so an answer
// names neither the owner nor anything outside the repositories. It
// returns the text and a count of what it rewrote, for the log.
func (p Paths) Generalize(text string) (string, int) {
	n := 0
	replace := func(old, with string) {
		if old == "" || old == "/" {
			return
		}
		n += strings.Count(text, old)
		text = strings.ReplaceAll(text, old, with)
	}
	source := strings.TrimRight(p.Source, "/")
	home := strings.TrimRight(p.Home, "/")
	if source != "" {
		replace(source+"/", "")
		if home != "" && strings.HasPrefix(source, home+"/") {
			replace("~/"+strings.TrimPrefix(source, home+"/")+"/", "")
			replace("~/"+strings.TrimPrefix(source, home+"/"), "the source folder")
		}
		replace(source, "the source folder")
	}
	if home != "" {
		replace(home+"/", "~/")
		replace(home, LocalPath)
	}
	text = localPath.ReplaceAllStringFunc(text, func(m string) string {
		i := strings.IndexAny(m, "~/")
		path := m[i:]
		end := strings.TrimRight(path, ".:!?") // a sentence's punctuation stays
		n++
		return m[:i] + LocalPath + path[len(end):]
	})
	return text, n
}
