package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveAPIKeyPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key")
	if err := os.WriteFile(keyPath, []byte("file-key\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	t.Setenv("BRAVE_API_KEY", "env-key")
	key, source, err := resolveAPIKey("cli-key", keyPath)
	if err != nil {
		t.Fatalf("resolveAPIKey returned error: %v", err)
	}
	if key != "cli-key" || source != "--api-key" {
		t.Fatalf("expected cli key, got key=%q source=%q", key, source)
	}

	key, source, err = resolveAPIKey("", keyPath)
	if err != nil {
		t.Fatalf("resolveAPIKey returned error: %v", err)
	}
	if key != "env-key" || source != "BRAVE_API_KEY" {
		t.Fatalf("expected env key, got key=%q source=%q", key, source)
	}

	t.Setenv("BRAVE_API_KEY", "")
	key, source, err = resolveAPIKey("", keyPath)
	if err != nil {
		t.Fatalf("resolveAPIKey returned error: %v", err)
	}
	if key != "file-key" || source != keyPath {
		t.Fatalf("expected file key, got key=%q source=%q", key, source)
	}
}

func TestResolveAPIKeyMissing(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "missing-key")

	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	if _, _, err := resolveAPIKey("", keyPath); err == nil {
		t.Fatal("expected error when no API key source is available")
	}
}

func TestCacheReadWriteAndExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	fullURL := "https://api.search.brave.com/res/v1/web/search?q=golang"
	version := "2025-09-22"
	body := []byte(`{"ok":true}`)

	if err := writeCache(tmpDir, fullURL, version, 200, body, 25*time.Millisecond); err != nil {
		t.Fatalf("writeCache failed: %v", err)
	}

	cached, hit, err := readCache(tmpDir, fullURL, version)
	if err != nil {
		t.Fatalf("readCache failed: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit")
	}
	if string(cached) != string(body) {
		t.Fatalf("unexpected cached body: %s", string(cached))
	}

	time.Sleep(40 * time.Millisecond)
	_, hit, err = readCache(tmpDir, fullURL, version)
	if err != nil {
		t.Fatalf("readCache after expiry failed: %v", err)
	}
	if hit {
		t.Fatal("expected cache miss after expiration")
	}
}

func TestRetryDelayFromHeaders(t *testing.T) {
	headers := map[string]string{
		"X-RateLimit-Reset": "2, 10",
	}
	delay := retryDelayFromHeaders(headers, 0, time.Second, 30*time.Second)
	if delay != 2*time.Second {
		t.Fatalf("expected 2s delay, got %v", delay)
	}
}

func TestPerformRequestWithRetry429ThenSuccess(t *testing.T) {
	attempts := 0
	sleepCalls := 0
	body, status, _, err := performRequestWithRetry(
		func() ([]byte, int, map[string]string, error) {
			attempts++
			if attempts == 1 {
				headers := map[string]string{"X-RateLimit-Reset": "0"}
				return nil, http.StatusTooManyRequests, headers, &apiError{
					StatusCode: http.StatusTooManyRequests,
					Body:       `{"error":"rate_limited"}`,
					Headers:    headers,
				}
			}
			return []byte(`{"ok":true}`), http.StatusOK, map[string]string{}, nil
		},
		requestRetryConfig{
			MaxRetries: 1,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
			Sleep: func(time.Duration) {
				sleepCalls++
			},
		},
	)
	if err != nil {
		t.Fatalf("performRequest returned error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", status)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if sleepCalls != 1 {
		t.Fatalf("expected one backoff sleep, got %d", sleepCalls)
	}
}

func TestPerformRequestWithRetryExhaustsRetriesOn429(t *testing.T) {
	attempts := 0
	_, status, _, err := performRequestWithRetry(
		func() ([]byte, int, map[string]string, error) {
			attempts++
			headers := map[string]string{"X-RateLimit-Reset": "0"}
			return nil, http.StatusTooManyRequests, headers, &apiError{
				StatusCode: http.StatusTooManyRequests,
				Body:       `{"error":"still_limited"}`,
				Headers:    headers,
			}
		},
		requestRetryConfig{
			MaxRetries: 2,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
			Sleep:      func(time.Duration) {},
		},
	)
	if err == nil {
		t.Fatal("expected error after retries are exhausted")
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected final status 429, got %d", status)
	}

	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected apiError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected apiError status 429, got %d", apiErr.StatusCode)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}
