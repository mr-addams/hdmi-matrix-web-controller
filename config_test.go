package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testMAC = "14:C4:72:A2:80:6A"

// doc собирает документ с одной матрицей; тесты меняют только то, что проверяют.
func doc(io IO) *Document {
	return &Document{Version: currentVersion, Devices: map[string]Device{testMAC: {IO: io}}}
}

func TestValidateAcceptsValidDocument(t *testing.T) {
	valid := doc(IO{
		In:  map[string]Port{"1": {Label: "Blu-ray", Tags: []Tag{{Text: "4K HDR"}}}},
		Out: map[string]Port{"8": {Tags: []Tag{{Text: "TV"}}}},
	})
	if err := validate(valid); err != nil {
		t.Fatalf("корректный документ отклонён: %v", err)
	}
}

func TestValidateRejectsBadMAC(t *testing.T) {
	bad := &Document{Version: currentVersion, Devices: map[string]Device{"14-C4-72-A2-80-6A": {}}}
	if err := validate(bad); err == nil {
		t.Fatal("MAC с дефисами принят")
	}
}

func TestValidateRejectsPortNumbers(t *testing.T) {
	for _, number := range []string{"0", "9", "10", "01", " 1", "x", ""} {
		bad := doc(IO{In: map[string]Port{number: {}}})
		if err := validate(bad); err == nil {
			t.Errorf("номер порта %q принят", number)
		}
	}
}

func TestValidateLabelLengthBoundary(t *testing.T) {
	atLimit := strings.Repeat("я", maxLabelRunes)
	if err := validate(doc(IO{In: map[string]Port{"1": {Label: atLimit}}})); err != nil {
		t.Errorf("подпись ровно %d символов отклонена: %v", maxLabelRunes, err)
	}
	overLimit := strings.Repeat("я", maxLabelRunes+1)
	if err := validate(doc(IO{In: map[string]Port{"1": {Label: overLimit}}})); err == nil {
		t.Errorf("подпись на %d символов принята", maxLabelRunes+1)
	}
}

func TestValidateTagCountBoundary(t *testing.T) {
	six := make([]Tag, maxTagsPerPort)
	seven := make([]Tag, maxTagsPerPort+1)
	for i := range six {
		six[i] = Tag{Text: "t"}
	}
	for i := range seven {
		seven[i] = Tag{Text: "t"}
	}
	if err := validate(doc(IO{Out: map[string]Port{"1": {Tags: six}}})); err != nil {
		t.Errorf("%d тегов отклонены: %v", maxTagsPerPort, err)
	}
	if err := validate(doc(IO{Out: map[string]Port{"1": {Tags: seven}}})); err == nil {
		t.Errorf("%d тегов приняты", maxTagsPerPort+1)
	}
}

func TestValidateTagTextRules(t *testing.T) {
	cases := map[string]string{
		"пустой":          "",
		"только пробелы":  "   ",
		"слишком длинный": strings.Repeat("a", maxTagRunes+1),
		"перевод строки":  "a\nb",
		"табуляция":       "a\tb",
	}
	for name, text := range cases {
		bad := doc(IO{In: map[string]Port{"1": {Tags: []Tag{{Text: text}}}}})
		if err := validate(bad); err == nil {
			t.Errorf("тег %s принят", name)
		}
	}
	ok := doc(IO{In: map[string]Port{"1": {Tags: []Tag{{Text: strings.Repeat("a", maxTagRunes)}}}}})
	if err := validate(ok); err != nil {
		t.Errorf("тег ровно %d символов отклонён: %v", maxTagRunes, err)
	}
}

func TestValidateRejectsWrongVersion(t *testing.T) {
	bad := doc(IO{})
	bad.Version = currentVersion + 1
	if err := validate(bad); err == nil {
		t.Fatal("неизвестная версия принята")
	}
}

func TestValidateRejectsTooManyDevices(t *testing.T) {
	devices := map[string]Device{}
	for i := 0; i <= maxDevices; i++ {
		devices[fakeMAC(i)] = Device{}
	}
	if err := validate(&Document{Version: currentVersion, Devices: devices}); err == nil {
		t.Fatalf("%d матриц приняты", maxDevices+1)
	}
}

func fakeMAC(i int) string {
	return "02:00:00:00:" + string("0123456789ABCDEF"[i/16%16]) + string("0123456789ABCDEF"[i%16])
}

func TestStoreLoadMissingFileReturnsEmpty(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "config.json")}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("отсутствующий файл дал ошибку: %v", err)
	}
	if loaded.Version != currentVersion || len(loaded.Devices) != 0 {
		t.Errorf("пустой документ не тот: %+v", loaded)
	}
}

func TestStoreSaveThenLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &Store{path: filepath.Join(dir, "config.json")}
	want := doc(IO{In: map[string]Port{"2": {Label: "XD4", Tags: []Tag{{Text: "Linux"}}}}})
	if err := store.Save(want); err != nil {
		t.Fatalf("сохранение: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}
	if got.Devices[testMAC].IO.In["2"].Label != "XD4" {
		t.Errorf("подпись не сохранилась: %+v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("во временном каталоге остались лишние файлы: %d", len(entries))
	}
}

func TestHandlerGetEmptyDocument(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET: код %d", recorder.Code)
	}
	var got Document
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got.Version != currentVersion {
		t.Errorf("GET вернул не документ: %v %s", err, recorder.Body.String())
	}
}

func TestHandlerPutThenGetDropsLegacyColor(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	// Так выглядела запись в старой версии страницы: у тега было поле color.
	body := `{"version":1,"devices":{"` + testMAC + `":{"io":{"in":{"1":{"label":"Blu-ray","tags":[{"text":"HDR","color":"#7aa2f7"}]}},"out":{}}}}}`

	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
	if put.Code != http.StatusNoContent {
		t.Fatalf("PUT: код %d, тело %s", put.Code, put.Body.String())
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if strings.Contains(get.Body.String(), "color") {
		t.Errorf("поле color попало в файл: %s", get.Body.String())
	}
	var got Document
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET вернул не JSON: %v", err)
	}
	if got.Devices[testMAC].IO.In["1"].Label != "Blu-ray" {
		t.Errorf("подпись не вернулась: %s", get.Body.String())
	}
}

func TestHandlerPutRejectsBadInput(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	cases := map[string]string{
		"не JSON":         `{"version":`,
		"плохой документ": `{"version":1,"devices":{"zz":{}}}`,
	}
	for name, body := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: код %d, ожидали 400", name, recorder.Code)
		}
	}
}

func TestHandlerPutRejectsOversizedBody(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	big := strings.Repeat("a", maxBodyBytes+1)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(big)))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("большое тело: код %d, ожидали 413", recorder.Code)
	}
}

func TestHandlerRejectsOtherMethods(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: код %d, ожидали 405", recorder.Code)
	}
}

func TestHandlerNormalizesMACCaseAndRejectsDuplicates(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	lower := `{"version":1,"devices":{"14:c4:72:a2:80:6a":{"io":{"in":{"1":{"label":"A"}}}}}}`
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(lower)))
	if put.Code != http.StatusNoContent {
		t.Fatalf("MAC в нижнем регистре: код %d, тело %s", put.Code, put.Body.String())
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var got Document
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Devices["14:C4:72:A2:80:6A"]; !ok || len(got.Devices) != 1 {
		t.Errorf("MAC не канонизирован: %+v", got.Devices)
	}

	twice := `{"version":1,"devices":{"14:C4:72:A2:80:6A":{},"14:c4:72:a2:80:6a":{}}}`
	dup := httptest.NewRecorder()
	handler.ServeHTTP(dup, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(twice)))
	if dup.Code != http.StatusBadRequest {
		t.Errorf("один MAC в двух регистрах: код %d, ожидали 400", dup.Code)
	}
}

func TestHandlerMinimalDocumentHasNoNulls(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"version":1}`)))
	if put.Code != http.StatusNoContent {
		t.Fatalf("PUT минимального документа: код %d", put.Code)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if strings.Contains(get.Body.String(), "null") {
		t.Errorf("в ответе null: %s", get.Body.String())
	}
}

func TestValidateRejectsInvisibleCharacters(t *testing.T) {
	cases := map[string]string{
		"ZWSP в теге":             "​",
		"bidi-override в теге":    "a‮b",
		"bidi-override в подписи": "‮abc",
	}
	for name, text := range cases {
		var bad *Document
		if strings.Contains(name, "подписи") {
			bad = doc(IO{In: map[string]Port{"1": {Label: text}}})
		} else {
			bad = doc(IO{In: map[string]Port{"1": {Tags: []Tag{{Text: text}}}}})
		}
		if err := validate(bad); err == nil {
			t.Errorf("%s принят", name)
		}
	}
}

func TestValidateRejectsWhitespaceLabel(t *testing.T) {
	bad := &Document{Version: currentVersion, Devices: map[string]Device{testMAC: {IO: IO{In: map[string]Port{"1": {Label: "   "}}}}}}
	normalized := &Document{Version: currentVersion, Devices: map[string]Device{testMAC: {IO: IO{In: map[string]Port{"1": {Label: "   "}}}}}}
	if err := normalize(normalized); err != nil {
		t.Fatal(err)
	}
	if err := validate(bad); err == nil {
		t.Error("подпись из пробелов принята до нормализации")
	}
	if err := validate(normalized); err != nil {
		t.Errorf("после нормализации подпись «   » должна стать пустой и пройти: %v", err)
	}
}

func TestValidateRejectsVersionZero(t *testing.T) {
	bad := doc(IO{})
	bad.Version = 0
	if err := validate(bad); err == nil {
		t.Fatal("версия 0 принята")
	}
}

func TestNormalizeTrimsTextAndKeepsEmptyCollections(t *testing.T) {
	document := doc(IO{In: map[string]Port{"1": {Label: "  TV  ", Tags: []Tag{{Text: " hdr "}}}}})
	if err := normalize(document); err != nil {
		t.Fatal(err)
	}
	port := document.Devices[testMAC].IO.In["1"]
	if port.Label != "TV" || port.Tags[0].Text != "hdr" {
		t.Errorf("пробелы не убраны: %+v", port)
	}
	if document.Devices[testMAC].IO.Out == nil {
		t.Error("пустые выходы остались nil")
	}
}

func TestValidateErrorIsDeterministic(t *testing.T) {
	bad := &Document{Version: currentVersion, Devices: map[string]Device{}}
	for i := 0; i < 5; i++ {
		bad.Devices[fakeMAC(i)] = Device{IO: IO{In: map[string]Port{"9": {}}}}
	}
	first := validate(bad).Error()
	for i := 0; i < 20; i++ {
		if got := validate(bad).Error(); got != first {
			t.Fatalf("ошибка меняется между запусками: %q и %q", first, got)
		}
	}
}

// TestMaxDocumentFitsBodyLimit считает худший допустимый документ и проверяет, что он проходит
// лимит тела. Без этого теста комментарий у maxBodyBytes мог бы снова стать неверным.
func TestMaxDocumentFitsBodyLimit(t *testing.T) {
	label := strings.Repeat("я", maxLabelRunes)
	tag := strings.Repeat("я", maxTagRunes)
	ports := map[string]Port{}
	for n := 1; n <= portCount; n++ {
		tags := make([]Tag, maxTagsPerPort)
		for i := range tags {
			tags[i] = Tag{Text: tag}
		}
		ports[string(rune('0'+n))] = Port{Label: label, Tags: tags}
	}
	document := &Document{Version: currentVersion, Devices: map[string]Device{}}
	for i := 0; i < maxDevices; i++ {
		document.Devices[fakeMAC(i)] = Device{IO: IO{In: ports, Out: ports}}
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxBodyBytes {
		t.Fatalf("худший документ %d байт больше лимита %d: поднимите maxBodyBytes или снизьте лимиты", len(data), maxBodyBytes)
	}
}

func TestHandlerAcceptsBodyExactlyAtLimit(t *testing.T) {
	handler := newHandler(&Store{path: filepath.Join(t.TempDir(), "config.json")})
	base := `{"version":1,"devices":{}}`
	body := base + strings.Repeat(" ", maxBodyBytes-len(base))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
	if recorder.Code != http.StatusNoContent {
		t.Errorf("тело ровно %d байт: код %d, ожидали 204", maxBodyBytes, recorder.Code)
	}
}

func TestStoreSaveFailsWithoutLeftovers(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "missing", "config.json")}
	if err := store.Save(doc(IO{})); err == nil {
		t.Fatal("сохранение в несуществующий каталог не вернуло ошибку")
	}
}

func TestHandlerGetReturnsErrorOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	newHandler(&Store{path: path}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("повреждённый файл: код %d, ожидали 500", recorder.Code)
	}
}

// TestConcurrentSaveAndLoad запускается под -race: параллельные PUT и GET не должны
// гоняться за файлом и не должны отдавать частично записанный документ.
func TestConcurrentSaveAndLoad(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.Save(doc(IO{})); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = store.Save(doc(IO{In: map[string]Port{"1": {Label: fmt.Sprintf("v%d", i)}}}))
		}(i)
		go func() {
			defer wg.Done()
			if _, err := store.Load(); err != nil {
				t.Errorf("параллельная загрузка: %v", err)
			}
		}()
	}
	wg.Wait()
}
