/*
Package access keeps the bot's users (spec 009): the people, other than
admins, who may talk to it in a channel, and whether each may also DM it.

It is changed only from the desktop, through the service, never from Slack.
Each user is an entry, so per-user grants (spec 010) can be added to it.
*/
package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ushineko/hikyaku/internal/atomicfile"
)

// User is one user's access.
type User struct {
	ID string `json:"id"`
	// DM lets the user DM the bot and be answered.
	DM bool `json:"dm,omitempty"`
}

// validID is a Slack user ID.
var validID = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)

// ErrBadID is returned for something that is not a Slack user ID.
var ErrBadID = errors.New("not a Slack user ID (U… or W…)")

// List is the users, kept in a JSON file. It is safe for concurrent use.
type List struct {
	path string

	mu    sync.Mutex
	users []User
}

// Open loads the list at path; a missing file is an empty list.
func Open(path string) (*List, error) {
	l := &List{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("read users: %w", err)
	}
	var v struct {
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("parse users %s: %w", path, err)
	}
	l.users = v.Users
	return l, nil
}

// Users is every user, sorted by ID.
func (l *List) Users() []User {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.users)
}

// Get is the user with the ID, if listed.
func (l *List) Get(id string) (User, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.IndexFunc(l.users, func(u User) bool { return u.ID == id })
	if i < 0 {
		return User{}, false
	}
	return l.users[i], true
}

// Put adds or replaces a user and saves.
func (l *List) Put(u User) error {
	u.ID = strings.ToUpper(strings.TrimSpace(u.ID))
	if !validID.MatchString(u.ID) {
		return ErrBadID
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.users = slices.DeleteFunc(l.users, func(x User) bool { return x.ID == u.ID })
	l.users = append(l.users, u)
	slices.SortFunc(l.users, func(a, b User) int { return strings.Compare(a.ID, b.ID) })
	return l.saveLocked()
}

// Remove removes a user and saves; it reports whether one was listed.
func (l *List) Remove(id string) (bool, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.users)
	l.users = slices.DeleteFunc(l.users, func(x User) bool { return x.ID == id })
	if len(l.users) == n {
		return false, nil
	}
	return true, l.saveLocked()
}

func (l *List) saveLocked() error {
	b, err := json.MarshalIndent(struct {
		Users []User `json:"users"`
	}{l.users}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode users: %w", err)
	}
	return atomicfile.Write(l.path, b)
}
