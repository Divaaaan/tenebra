package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"sync"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// The composition owns root and the canonical writer lock for this process.
// This measured cache owns only netstrategy.json, never business preferences.
type rootNetStrategies struct {
	mu       sync.Mutex
	root     *securefs.Root
	values   map[string]string
	poisoned bool
	warn     func(error)
}

func OpenRootNetStrategies(root *securefs.Root, warn func(error)) (netStrategyStore, error) {
	if root == nil {
		return nil, securefs.ErrUntrusted
	}
	cache := &rootNetStrategies{root: root, values: map[string]string{}, warn: warn}
	data, err := root.ReadFile(netStrategyFile, 1<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return cache, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if first, err := decoder.Token(); err != nil || first != json.Delim('{') {
		return nil, errors.New("invalid network strategy cache")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid network strategy cache")
		}
		name, ok := token.(string)
		valueToken, valueErr := decoder.Token()
		value, valueOK := valueToken.(string)
		if !ok || name == "" || len(name) > 1024 || len(cache.values) >= 8192 || valueErr != nil || !valueOK || len(value) > 256 {
			return nil, errors.New("invalid network strategy cache")
		}
		if _, exists := cache.values[name]; exists {
			return nil, errors.New("duplicate network strategy cache entry")
		}
		cache.values[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid network strategy cache")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid network strategy cache")
	}
	return cache, nil
}
func (c *rootNetStrategies) Get(network string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return "", false
	}
	value, ok := c.values[network]
	return value, ok
}
func (c *rootNetStrategies) Set(network, strategy string) {
	c.mu.Lock()
	if c.poisoned || network == "" || len(network) > 1024 || len(strategy) > 256 {
		c.mu.Unlock()
		return
	}
	if old, ok := c.values[network]; ok && old == strategy {
		c.mu.Unlock()
		return
	}
	next := make(map[string]string, len(c.values)+1)
	for name, value := range c.values {
		next[name] = value
	}
	next[network] = strategy
	data, err := json.Marshal(next)
	if err == nil && (len(data) > 1<<20 || len(next) > 8192) {
		err = securefs.ErrLimit
	}
	if err == nil {
		outcome, writeErr := c.root.ReplaceFile(netStrategyFile, data)
		err = writeErr
		if outcome == securefs.Committed && err == nil {
			c.values = next
		} else if outcome != securefs.Unchanged {
			c.poisoned = true
			err = errors.Join(errors.New("network strategy cache durability unknown"), err)
		} else if err == nil {
			err = securefs.ErrUntrusted
		}
	}
	c.mu.Unlock()
	if err != nil && c.warn != nil {
		c.warn(err)
	}
}
