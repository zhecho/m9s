package mail

import "github.com/zhecho/m9s/internal/config"

type Account struct {
	Name   string
	Config *config.Account
}

type Manager struct {
	accounts []*Account
	idx      int
}

func NewManager(accounts []*Account) *Manager {
	return &Manager{accounts: accounts}
}

func (m *Manager) Active() *Account {
	if len(m.accounts) == 0 {
		return nil
	}
	return m.accounts[m.idx]
}

func (m *Manager) All() []*Account {
	return m.accounts
}

func (m *Manager) Next() {
	if len(m.accounts) == 0 {
		return
	}
	m.idx = (m.idx + 1) % len(m.accounts)
}

func (m *Manager) Prev() {
	if len(m.accounts) == 0 {
		return
	}
	m.idx = (m.idx - 1 + len(m.accounts)) % len(m.accounts)
}

func (m *Manager) SetActive(name string) {
	for i, a := range m.accounts {
		if a.Name == name {
			m.idx = i
			return
		}
	}
}
