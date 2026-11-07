package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The limits match the ones the page enforces: the server must not accept more than
// the interface can show in its 8×8 grid.
const (
	currentVersion = 1
	// Worst case: 64 matrices with 16 ports each, every port with a 32-character label and six 16-character tags
	// (Cyrillic takes two bytes per character). TestMaxDocumentFitsBodyLimit
	// computes this size and fails if the limit becomes smaller.
	maxBodyBytes   = 512 << 10
	maxDevices     = 64
	maxLabelRunes  = 32
	maxTagRunes    = 16
	maxTagsPerPort = 6
	portCount      = 8
)

// macPattern accepts a colon-separated MAC in any case: the matrix sends it that way, and the page stores it
// that way too. The canonical form is upper case, set by normalize.
var macPattern = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// Document is the whole settings file. The version field lets an old file be rejected
// instead of being parsed as a new format.
type Document struct {
	// Version is the format version; it is always currentVersion for now.
	Version int `json:"version"`
	// Devices holds the settings per matrix, keyed by canonical MAC (upper case, colons).
	Devices map[string]Device `json:"devices"`
}

// Device holds the settings of one matrix.
type Device struct {
	// IO holds the labels of this matrix's inputs and outputs.
	IO IO `json:"io"`
}

// IO holds the labels of inputs and outputs. Keys are port numbers as strings, as the page sends them.
type IO struct {
	// In holds the inputs, keyed "1".."8".
	In map[string]Port `json:"in"`
	// Out holds the outputs, keyed "1".."8".
	Out map[string]Port `json:"out"`
}

// Port holds the label and tags of one input or output.
type Port struct {
	// Label is the label; an empty string means the default name.
	Label string `json:"label"`
	// Tags are kept in the order they were added.
	Tags []Tag `json:"tags"`
}

// Tag holds only the text. An older page version also stored a color here; unknown fields are
// dropped during parsing, so old records move to the new format without a color.
type Tag struct {
	// Text is the tag text; it cannot be empty.
	Text string `json:"text"`
}

// normalize brings a document to canonical form before validation and writing. MACs are upper case,
// otherwise one matrix could end up under two keys. Empty collections are objects and arrays,
// not null, because a client expecting an object would fail on null.
func normalize(doc *Document) error {
	devices := make(map[string]Device, len(doc.Devices))
	for mac, device := range doc.Devices {
		canonical := strings.ToUpper(mac)
		if _, duplicate := devices[canonical]; duplicate {
			return fmt.Errorf("MAC %s указан дважды в разном регистре", canonical)
		}
		device.IO.In = normalizePorts(device.IO.In)
		device.IO.Out = normalizePorts(device.IO.Out)
		devices[canonical] = device
	}
	doc.Devices = devices
	return nil
}

// normalizePorts trims spaces around labels and tags: the file stores exactly what is shown.
func normalizePorts(ports map[string]Port) map[string]Port {
	if ports == nil {
		return map[string]Port{}
	}
	for number, port := range ports {
		port.Label = strings.TrimSpace(port.Label)
		if port.Tags == nil {
			port.Tags = []Tag{}
		}
		for i := range port.Tags {
			port.Tags[i].Text = strings.TrimSpace(port.Tags[i].Text)
		}
		ports[number] = port
	}
	return ports
}

// validate checks the whole document and returns the first error: one reason is enough for the client,
// and a full list would complicate the response. Maps are walked in sorted key order,
// otherwise the same document would produce different messages from run to run.
func validate(doc *Document) error {
	if doc.Version != currentVersion {
		return fmt.Errorf("версия %d не поддерживается", doc.Version)
	}
	if len(doc.Devices) > maxDevices {
		return fmt.Errorf("матриц больше %d", maxDevices)
	}
	for _, mac := range sortedKeys(doc.Devices) {
		if !macPattern.MatchString(mac) {
			return fmt.Errorf("некорректный MAC %q", mac)
		}
		device := doc.Devices[mac]
		if err := validatePorts(device.IO.In); err != nil {
			return fmt.Errorf("%s, входы: %w", mac, err)
		}
		if err := validatePorts(device.IO.Out); err != nil {
			return fmt.Errorf("%s, выходы: %w", mac, err)
		}
	}
	return nil
}

func sortedKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validatePorts(ports map[string]Port) error {
	for _, number := range sortedKeys(ports) {
		if !validPortNumber(number) {
			return fmt.Errorf("номер порта %q вне 1–%d", number, portCount)
		}
		port := ports[number]
		// A label may be empty (meaning the default name), but not made of spaces only.
		if port.Label != "" && strings.TrimSpace(port.Label) == "" {
			return fmt.Errorf("порт %s: подпись из одних пробелов", number)
		}
		if err := validateText(port.Label, maxLabelRunes); err != nil {
			return fmt.Errorf("порт %s, подпись: %w", number, err)
		}
		if len(port.Tags) > maxTagsPerPort {
			return fmt.Errorf("порт %s: тегов больше %d", number, maxTagsPerPort)
		}
		for _, tag := range port.Tags {
			// An empty tag makes no sense: it would show no dot and no text, and look broken in the list.
			if strings.TrimSpace(tag.Text) == "" {
				return fmt.Errorf("порт %s: пустой тег", number)
			}
			if err := validateText(tag.Text, maxTagRunes); err != nil {
				return fmt.Errorf("порт %s, тег: %w", number, err)
			}
		}
	}
	return nil
}

// validPortNumber accepts a port as a single digit 1–8. Leading zeros and spaces are not accepted,
// otherwise one port could be stored under two keys.
func validPortNumber(number string) bool {
	return len(number) == 1 && number[0] >= '1' && number[0] <= '0'+portCount
}

// validateText limits the length and rejects characters that break the display: control characters
// (a newline would break the grid) and format characters Cf (ZWSP and bidi overrides are invisible but change
// how a heading looks). Spaces at the edges have already been removed by normalize.
func validateText(text string, maxRunes int) error {
	if utf8.RuneCountInString(text) > maxRunes {
		return fmt.Errorf("длиннее %d символов", maxRunes)
	}
	for _, char := range text {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return errors.New("управляющий или невидимый символ")
		}
	}
	return nil
}

// Store keeps the document in one file. Writes are atomic: if the process crashes, the old file
// stays intact instead of being half-overwritten.
type Store struct {
	path string
	mu   sync.Mutex
}

// Load reads the document. A missing file is not an error: the first run starts with empty settings.
func (s *Store) Load() (*Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &Document{Version: currentVersion, Devices: map[string]Device{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("файл настроек повреждён: %w", err)
	}
	return &doc, nil
}

// Save writes the document to a temporary file in the same directory, flushes it to disk, and only then
// renames it. A rename within one directory is atomic, so a reader sees either
// the old file or the new one. The directory is flushed after the rename; otherwise a power loss could leave
// the renamed file missing from disk.
func (s *Store) Save(doc *Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// After a successful rename the temporary file no longer exists, so Remove returns an error. That is expected: the goal is
	// to clean up the temporary file if something went wrong.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// newHandler builds the HTTP interface. Routes include the method in the pattern, so a wrong method gets 405
// from the router itself, without our code.
func newHandler(store *Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		doc, err := store.Load()
		if err != nil {
			log.Printf("чтение настроек: %v", err)
			http.Error(w, "не удалось прочитать настройки", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			log.Printf("ответ настроек: %v", err)
		}
	})

	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		// The body limit is applied before parsing; otherwise a large JSON would use memory before any checks run.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, fmt.Sprintf("настройки больше %d КиБ", maxBodyBytes>>10), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "не удалось прочитать тело запроса", http.StatusBadRequest)
			return
		}
		var doc Document
		if err := json.Unmarshal(body, &doc); err != nil {
			http.Error(w, "некорректный JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := normalize(&doc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validate(&doc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.Save(&doc); err != nil {
			log.Printf("запись настроек: %v", err)
			http.Error(w, "не удалось сохранить настройки", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}
