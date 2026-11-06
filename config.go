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

// Лимиты совпадают с теми, что проверяет страница: сервер не должен принимать больше,
// чем интерфейс может показать в колонке 8×8.
const (
	currentVersion = 1
	// Худший случай — 64 матрицы по 16 портов, каждый с подписью на 32 символа и шестью тегами
	// по 16 символов (кириллица занимает по два байта). Тест TestMaxDocumentFitsBodyLimit
	// считает этот размер и падает, если лимит станет меньше.
	maxBodyBytes   = 512 << 10
	maxDevices     = 64
	maxLabelRunes  = 32
	maxTagRunes    = 16
	maxTagsPerPort = 6
	portCount      = 8
)

// macPattern принимает MAC с двоеточиями в любом регистре: так его присылает матрица и так его
// хранит страница. Канонический вид — верхний регистр, его задаёт normalize.
var macPattern = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// Document — весь файл настроек. Поле version нужно, чтобы при смене формата старый файл
// не разобрали как новый.
type Document struct {
	// Version — версия формата; сейчас всегда currentVersion.
	Version int `json:"version"`
	// Devices — настройки матриц по каноническому MAC (верхний регистр, двоеточия).
	Devices map[string]Device `json:"devices"`
}

// Device — настройки одной матрицы.
type Device struct {
	// IO — подписи входов и выходов этой матрицы.
	IO IO `json:"io"`
}

// IO — подписи входов и выходов. Ключи — номера портов строкой, как их отдаёт страница.
type IO struct {
	// In — входы, ключи "1".."8".
	In map[string]Port `json:"in"`
	// Out — выходы, ключи "1".."8".
	Out map[string]Port `json:"out"`
}

// Port — подпись и теги одного входа или выхода.
type Port struct {
	// Label — подпись; пустая строка значит «как по умолчанию».
	Label string `json:"label"`
	// Tags — теги в порядке добавления.
	Tags []Tag `json:"tags"`
}

// Tag — только текст. Поле цвета страница раньше хранила в записи; неизвестные поля при разборе
// просто отбрасываются, поэтому старые записи переходят в новый формат без цвета.
type Tag struct {
	// Text — текст тега; пустой быть не может.
	Text string `json:"text"`
}

// normalize приводит документ к каноническому виду до проверки и записи. MAC в верхнем регистре:
// иначе одна матрица оказалась бы в файле под двумя ключами. Пустые коллекции — объекты и массивы,
// а не null: клиент, который ждёт объект, падал бы на null.
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

// normalizePorts убирает пробелы по краям подписей и тегов: в файл пишем то, что покажем.
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

// validate проверяет документ целиком и возвращает первую найденную ошибку: клиенту хватает
// одной причины, а список всех ошибок усложнил бы ответ. Обход map идёт по отсортированным
// ключам, иначе один и тот же документ давал бы разные сообщения от запуска к запуску.
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
		// Подпись может быть пустой (значит «как по умолчанию»), но из одних пробелов — нет.
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
			// Пустой тег не имеет смысла: точки и текста не будет, а в списке он выглядел бы сломанным.
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

// validPortNumber — порт в виде строки из одной цифры 1–8. Ведущие нули и пробелы не принимаем,
// иначе один порт мог бы оказаться записан под двумя ключами.
func validPortNumber(number string) bool {
	return len(number) == 1 && number[0] >= '1' && number[0] <= '0'+portCount
}

// validateText ограничивает длину и отсекает символы, которые ломают отображение: управляющие
// (перевод строки ломал бы сетку) и формата Cf (ZWSP и bidi-override не видны глазом, но меняют
// вид заголовка). Пробелы по краям к этому моменту уже убраны normalize.
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

// Store хранит документ в одном файле. Запись атомарная: при падении процесса старый файл
// остаётся целым, а не наполовину переписанным.
type Store struct {
	path string
	mu   sync.Mutex
}

// Load читает документ. Отсутствующий файл — не ошибка: первый запуск начинается с пустых настроек.
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

// Save пишет документ во временный файл в том же каталоге, сбрасывает его на диск и только
// потом переименовывает. rename внутри одного каталога атомарен, поэтому читатель видит либо
// старый файл, либо новый. Каталог сбрасываем после rename: иначе при потере питания сам
// переименованный файл может не попасть на диск.
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
	// После успешного rename файла уже нет, и Remove вернёт ошибку. Это нормально: цель —
	// убрать временный файл, если что-то пошло не так.
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

// newHandler собирает HTTP-интерфейс. Маршруты с методом в шаблоне: чужой метод получит 405
// от самого роутера, без нашего кода.
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
		// Лимит тела ограничиваем до разбора: иначе большой JSON занял бы память раньше проверок.
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
