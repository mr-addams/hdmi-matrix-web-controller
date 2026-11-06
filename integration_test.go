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

// Интеграционный тест: настоящий HTTP-сервер и файл на диске. Проверяет, что данные переживают
// перезапуск сервиса — ради этого сервис и существует.
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

	// Новый экземпляр сервиса на том же файле — как после docker restart.
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
