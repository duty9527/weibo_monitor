package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Health is independent of systemd liveness and contains no cookies or messages.
func writeSubscriptionHealth(path, phase string) error {
	data, err := json.Marshal(map[string]string{"phase": phase, "updated_at": time.Now().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".health-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
