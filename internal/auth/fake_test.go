package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"zkscan/internal/core"
)

// fakeStore: core.Store ในหน่วยความจำ; method ที่ไม่ได้ implement จะ panic (nil embedded)
type fakeStore struct {
	core.Store
	mu       sync.Mutex
	nextID   int64
	users    map[int64]core.User
	sessions map[string]core.Session
	keys     map[int64]core.APIKey
	logs     []core.LogEntry

	touchCalls   []time.Time // TouchAPIKey
	deleteExpiry int
	failGetUser  error // ถ้าตั้ง GetUserByUsername/GetUserByID คืน error นี้
	failSession  error // GetSession
	failKey      error // GetAPIKeyByHash
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    map[int64]core.User{},
		sessions: map[string]core.Session{},
		keys:     map[int64]core.APIKey{},
	}
}

func (f *fakeStore) id() int64 { f.nextID++; return f.nextID }

func (f *fakeStore) CountUsers(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

func (f *fakeStore) CreateUser(_ context.Context, u core.User) (core.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.users {
		if x.Username == u.Username {
			return core.User{}, core.ErrConflict
		}
	}
	u.ID = f.id()
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeStore) GetUserByID(_ context.Context, id int64) (core.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGetUser != nil {
		return core.User{}, f.failGetUser
	}
	u, ok := f.users[id]
	if !ok {
		return core.User{}, core.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) GetUserByUsername(_ context.Context, name string) (core.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGetUser != nil {
		return core.User{}, f.failGetUser
	}
	for _, u := range f.users {
		if u.Username == name {
			return u, nil
		}
	}
	return core.User{}, core.ErrNotFound
}

func (f *fakeStore) UpdatePassword(_ context.Context, id int64, hash string, must bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return core.ErrNotFound
	}
	u.PasswordHash, u.MustChangePassword = hash, must
	f.users[id] = u
	return nil
}

func (f *fakeStore) TouchLogin(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return core.ErrNotFound
	}
	u.LastLoginAt = &at
	f.users[id] = u
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, s core.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.TokenHash] = s
	return nil
}

func (f *fakeStore) GetSession(_ context.Context, h string) (core.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSession != nil {
		return core.Session{}, f.failSession
	}
	s, ok := f.sessions[h]
	if !ok {
		return core.Session{}, core.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, h string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[h]; !ok {
		return core.ErrNotFound
	}
	delete(f.sessions, h)
	return nil
}

func (f *fakeStore) DeleteUserSessions(_ context.Context, uid int64, except string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h, s := range f.sessions {
		if s.UserID == uid && h != except {
			delete(f.sessions, h)
		}
	}
	return nil
}

func (f *fakeStore) DeleteExpiredSessions(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteExpiry++
	for h, s := range f.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(f.sessions, h)
		}
	}
	return nil
}

func (f *fakeStore) CreateAPIKey(_ context.Context, k core.APIKey) (core.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k.ID = f.id()
	f.keys[k.ID] = k
	return k, nil
}

func (f *fakeStore) GetAPIKeyByHash(_ context.Context, h string) (core.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKey != nil {
		return core.APIKey{}, f.failKey
	}
	for _, k := range f.keys {
		if k.KeyHash == h {
			return k, nil
		}
	}
	return core.APIKey{}, core.ErrNotFound
}

func (f *fakeStore) RevokeAPIKey(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[id]
	if !ok {
		return core.ErrNotFound
	}
	k.Revoked = true
	f.keys[id] = k
	return nil
}

func (f *fakeStore) TouchAPIKey(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[id]
	if !ok {
		return core.ErrNotFound
	}
	k.LastUsedAt = &at
	f.keys[id] = k
	f.touchCalls = append(f.touchCalls, at)
	return nil
}

func (f *fakeStore) AddLog(_ context.Context, e core.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, e)
	return nil
}

func (f *fakeStore) logText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := ""
	for _, e := range f.logs {
		s += e.Level + "|" + e.Source + "|" + e.Message + "\n"
	}
	return s
}

func (f *fakeStore) sessionCount(uid int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.UserID == uid {
			n++
		}
	}
	return n
}

var errBoom = errors.New("boom")

// fakeClock: นาฬิกาที่เดินเองได้
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
