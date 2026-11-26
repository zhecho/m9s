package keepassxc

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// fakeServer is a scripted KeePassXC: it does the real key exchange and
// encryption, and answers from the fields the test sets.
type fakeServer struct {
	t    *testing.T
	path string
	ln   net.Listener

	mu                 sync.Mutex
	locked             bool
	assoc              map[string]string  // accepted id → key
	assocName          string             // id handed out by associate
	logins             map[string][]Login // by url
	noticesBeforeReply []string           // sent before the next get-logins reply
	closeOn            string             // action on which the connection is dropped unanswered
	silentOn           string             // action that is never answered (KeePassXC showing a dialog)
	seen               []string           // every url asked for
	unlockAsked        []bool             // triggerUnlock of every get-databasehash
	setLogins          []map[string]any   // decrypted body of every set-login
	groups             []Group            // the database's group tree, root first
	nextUUID           int
	conns              []net.Conn
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket paths are short; a test's temp dir can be too long.
	sockDir, err := mkShortDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, path: filepath.Join(sockDir, socketName), assoc: map[string]string{},
		assocName: "m9s", logins: map[string][]Login{}}
	s.ln, err = net.Listen("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ln.Close(); s.closeConns() })
	go func() {
		for {
			c, err := s.ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeServer) closeConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

// unlock opens the database and tells every client, as KeePassXC does.
func (s *fakeServer) unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked = false
	for _, c := range s.conns {
		json.NewEncoder(c).Encode(map[string]any{"action": "database-unlocked"})
	}
}

func (s *fakeServer) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *fakeServer) serve(c net.Conn) {
	dec := json.NewDecoder(c)
	var clientPub [32]byte
	pub, priv, _ := box.GenerateKey(rand.Reader)
	send := func(v map[string]any) { json.NewEncoder(c).Encode(v) }
	associatedHere := false // KeePassXC answers group questions only on an approved connection
	for {
		var req map[string]any
		if dec.Decode(&req) != nil {
			return
		}
		action, _ := req["action"].(string)
		s.mu.Lock()
		if s.closeOn == action {
			s.mu.Unlock()
			c.Close()
			return
		}
		if s.silentOn == action {
			s.mu.Unlock()
			continue
		}
		if action == "change-public-keys" {
			raw, _ := b64.DecodeString(req["publicKey"].(string))
			copy(clientPub[:], raw)
			send(map[string]any{"action": action, "publicKey": b64.EncodeToString(pub[:]), "success": "true", "version": "2.8.0"})
			s.mu.Unlock()
			continue
		}
		var n [24]byte
		rawN, _ := b64.DecodeString(req["nonce"].(string))
		copy(n[:], rawN)
		sealed, _ := b64.DecodeString(req["message"].(string))
		plain, ok := box.Open(nil, sealed, &n, &clientPub, priv)
		if !ok {
			s.mu.Unlock()
			s.t.Errorf("fake KeePassXC could not decrypt %s", action)
			return
		}
		var body map[string]any
		json.Unmarshal(plain, &body)
		if body["action"] != action {
			s.t.Errorf("encrypted body action = %v, want %s", body["action"], action)
		}

		fail := func(code, msg string) {
			send(map[string]any{"action": action, "errorCode": code, "error": msg})
		}
		reply := func(v map[string]any) {
			rn := incremented(n)
			out, _ := json.Marshal(v)
			send(map[string]any{"action": action, "nonce": b64.EncodeToString(rn[:]),
				"message": b64.EncodeToString(box.Seal(nil, out, &rn, &clientPub, priv))})
		}
		known := func(id, key any) bool {
			i, _ := id.(string)
			k, _ := key.(string)
			return i != "" && s.assoc[i] == k
		}

		switch action {
		case "get-databasehash":
			s.unlockAsked = append(s.unlockAsked, req["triggerUnlock"] == "true")
			if s.locked {
				fail("1", "Database not opened")
			} else {
				reply(map[string]any{"hash": "abc", "success": "true"})
			}
		case "associate":
			key, _ := body["idKey"].(string)
			s.assoc[s.assocName] = key
			associatedHere = true
			reply(map[string]any{"id": s.assocName, "hash": "abc", "success": "true"})
		case "test-associate":
			if known(body["id"], body["key"]) {
				associatedHere = true
				reply(map[string]any{"id": body["id"], "success": "true"})
			} else {
				fail("8", "KeePassXC association failed, try again")
			}
		case "get-logins":
			url, _ := body["url"].(string)
			s.seen = append(s.seen, url)
			for _, notice := range s.noticesBeforeReply {
				send(map[string]any{"action": notice})
			}
			s.noticesBeforeReply = nil
			keys, _ := body["keys"].([]any)
			allowed := false
			for _, k := range keys {
				if m, ok := k.(map[string]any); ok && known(m["id"], m["key"]) {
					allowed = true
				}
			}
			switch {
			case s.locked:
				fail("1", "Database not opened")
			case !allowed:
				fail("8", "KeePassXC association failed, try again")
			case len(s.logins[url]) == 0:
				fail("15", "No logins found")
			default:
				var entries []any
				for _, l := range s.logins[url] {
					entries = append(entries, map[string]any{"name": l.Name, "login": l.User,
						"password": l.Password, "uuid": l.UUID, "group": l.Group})
				}
				reply(map[string]any{"count": len(entries), "entries": entries, "success": "true"})
			}
		case "get-database-groups":
			switch {
			case s.locked:
				fail("1", "Database not opened")
			case !associatedHere:
				fail("8", "KeePassXC association failed, try again")
			default:
				// As KeePassXC nests it: {"groups": {"groups": [root]}}.
				reply(map[string]any{"groups": map[string]any{"groups": s.groups}, "success": "true"})
			}
		case "set-login":
			s.setLogins = append(s.setLogins, body)
			id, _ := body["id"].(string)
			url, _ := body["url"].(string)
			uuid, _ := body["uuid"].(string)
			l := Login{User: str(body, "login"), Password: str(body, "password"), UUID: uuid}
			switch {
			case s.locked:
				fail("1", "Database not opened")
			case id == "" || s.assoc[id] == "":
				fail("8", "KeePassXC association failed, try again")
			case uuid == "":
				s.nextUUID++
				l.UUID = fmt.Sprintf("uuid-%d", s.nextUUID)
				s.logins[url] = append(s.logins[url], l)
				reply(map[string]any{"count": nil, "entries": nil, "error": "", "success": "true"})
			default:
				found := false
				for i := range s.logins[url] {
					if s.logins[url][i].UUID == uuid {
						s.logins[url][i] = l
						found = true
					}
				}
				if !found {
					fail("15", "No logins found")
				} else {
					reply(map[string]any{"count": nil, "entries": nil, "error": "", "success": "true"})
				}
			}
		default:
			fail("3", "Incorrect action")
		}
		s.mu.Unlock()
	}
}
