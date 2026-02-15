package rules

import (
	"strings"
	"sync"
)

type Rule struct {
	Contains string `json:"contains"`
	Reply    string `json:"reply"`
}

type Store struct {
	mu    sync.RWMutex
	rules []Rule
}

func NewStore(seed []Rule) *Store {
	copySeed := append([]Rule(nil), seed...)
	return &Store{rules: copySeed}
}

func (s *Store) Match(content string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lowered := strings.ToLower(content)
	for _, rule := range s.rules {
		if strings.Contains(lowered, strings.ToLower(rule.Contains)) {
			return rule.Reply, true
		}
	}
	return "", false
}

func (s *Store) Set(items []Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append([]Rule(nil), items...)
}

func (s *Store) All() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Rule(nil), s.rules...)
}
