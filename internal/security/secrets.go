package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

type SecretStore struct {
	service  string
	fallback string
	mu       sync.Mutex
}

func NewSecretStore(service, fallbackDir string) *SecretStore {
	return &SecretStore{service: service, fallback: filepath.Join(fallbackDir, "secrets.json")}
}

func (s *SecretStore) Get(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, err := keyring.Get(s.service, name); err == nil && value != "" {
		return value, nil
	}
	values, err := s.readFallback()
	if err != nil {
		return "", err
	}
	value := values[name]
	if value == "" {
		return "", os.ErrNotExist
	}
	return value, nil
}

func (s *SecretStore) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyringErr := keyring.Set(s.service, name, value)
	if keyringErr == nil {
		// Do not keep a second plaintext copy when the OS keychain works. Remove
		// an older fallback entry so a later keychain failure cannot resurrect a
		// stale credential.
		if err := s.removeFallback(name); err != nil {
			return fmt.Errorf("remove stale secret fallback: %w", err)
		}
		return nil
	}
	// Headless Linux services and locked-down desktops may not provide a
	// usable keychain. Fall back to a 0600 file so the bridge remains usable,
	// but never duplicate secrets when the keychain is available.
	if err := s.mirrorFallback(name, value); err != nil {
		return fmt.Errorf("save secret: keychain and fallback failed: %v; %w", keyringErr, err)
	}
	return nil
}

func (s *SecretStore) mirrorFallback(name, value string) error {
	values, err := s.readFallback()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if values == nil {
		values = map[string]string{}
	}
	values[name] = value
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.fallback), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(s.fallback, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(b)
	return err
}

func (s *SecretStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = keyring.Delete(s.service, name)
	values, err := s.readFallback()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	delete(values, name)
	if len(values) == 0 {
		if err := os.Remove(s.fallback); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return s.writeFallback(b)
}

func (s *SecretStore) removeFallback(name string) error {
	values, err := s.readFallback()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, exists := values[name]; !exists {
		return nil
	}
	delete(values, name)
	if len(values) == 0 {
		return os.Remove(s.fallback)
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return s.writeFallback(b)
}

func (s *SecretStore) writeFallback(b []byte) error {
	file, err := os.OpenFile(s.fallback, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(b)
	return err
}

func (s *SecretStore) readFallback() (map[string]string, error) {
	if info, err := os.Stat(s.fallback); err == nil && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(s.fallback, 0o600); err != nil {
			return nil, fmt.Errorf("secure fallback secrets: %w", err)
		}
	}
	b, err := os.ReadFile(s.fallback)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read fallback secrets: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, fmt.Errorf("parse fallback secrets: %w", err)
	}
	return values, nil
}
