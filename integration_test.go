package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// Integration test: a real HTTP server and a real file on disk. It checks that data survives
// a service restart, which is the whole point of the service.
func TestSettingsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := []byte(`{"version":1,"devices":{"` + testMAC + `":{"io":{"out":{"3":{"label":"Проектор","tags":[{"text":"зал"}]}}}}}}`)

	first := httptest.NewServer(newHandler(&Store{path: path}))
	request, _ := http.NewRequest(http.MethodPut, first.URL+"/api/config", bytes.NewReader(body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	response.Body.Close()
	first.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: код %d", response.StatusCode)
	}

	// A new service instance on the same file, as after docker restart.
	second := httptest.NewServer(newHandler(&Store{path: path}))
	defer second.Close()
	response, err = http.Get(second.URL + "/api/config")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)

	var got Document
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("GET: не JSON: %v", err)
	}
	if got.Devices[testMAC].IO.Out["3"].Label != "Проектор" {
		t.Errorf("после перезапуска подпись потеряна: %s", data)
	}
}
