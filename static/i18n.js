// Языки интерфейса. Словари лежат в lang/<код>.json; русский подгружается всегда,
// как запасной вариант, поэтому пропущенный ключ в другом языке не выводит сырой ключ.

export const LANGUAGES = [
  { code: "uk", name: "Українська", locale: "uk-UA" },
  { code: "en", name: "English", locale: "en-GB" },
  { code: "de", name: "Deutsch", locale: "de-DE" },
  { code: "fr", name: "Français", locale: "fr-FR" },
  { code: "es", name: "Español", locale: "es-ES" },
  { code: "it", name: "Italiano", locale: "it-IT" },
  { code: "pt", name: "Português", locale: "pt-PT" },
  { code: "ja", name: "日本語", locale: "ja-JP" },
  { code: "ko", name: "한국어", locale: "ko-KR" },
  { code: "zh", name: "中文", locale: "zh-CN" },
  { code: "ru", name: "Русский", locale: "ru-RU" },
];

// Язык по умолчанию и запасной словарь для пропущенных ключей.
const FALLBACK = "en";
const STORE_KEY = "hdmi-matrix.lang";
const dictionaries = {};
let current = FALLBACK;

async function loadDictionary(code) {
  if (!dictionaries[code]) {
    const response = await fetch(`lang/${code}.json`, { cache: "no-cache" });
    if (!response.ok) throw new Error(`не загрузился язык ${code}`);
    dictionaries[code] = await response.json();
  }
  return dictionaries[code];
}

function storedLanguage() {
  try {
    return localStorage.getItem(STORE_KEY);
  } catch {
    return null; // localStorage может быть недоступен: язык выберем по браузеру
  }
}

// Язык по умолчанию — английский. Сохранённый выбор пользователя имеет приоритет.
function detectLanguage() {
  const stored = storedLanguage();
  return LANGUAGES.some((language) => language.code === stored) ? stored : FALLBACK;
}

// Подстановка параметров: t("err.noResponse", { ip, status }) → "нет ответа по …".
// Неизвестный параметр оставляем как есть, чтобы ошибка в тексте была видна.
export function t(key, params = {}) {
  const template = dictionaries[current]?.[key] ?? dictionaries[FALLBACK]?.[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (match, name) => (name in params ? String(params[name]) : match));
}

export function currentLanguage() {
  return LANGUAGES.find((language) => language.code === current);
}

// Текст для элементов с data-i18n (текст), data-i18n-placeholder и data-i18n-aria.
export function applyStaticTexts(root = document) {
  for (const element of root.querySelectorAll("[data-i18n]")) element.textContent = t(element.dataset.i18n);
  for (const element of root.querySelectorAll("[data-i18n-placeholder]")) {
    element.placeholder = t(element.dataset.i18nPlaceholder);
  }
  for (const element of root.querySelectorAll("[data-i18n-aria]")) {
    element.setAttribute("aria-label", t(element.dataset.i18nAria));
  }
}

export async function setLanguage(code) {
  await Promise.all([loadDictionary(FALLBACK), loadDictionary(code)]);
  current = code;
  document.documentElement.lang = code;
  try {
    localStorage.setItem(STORE_KEY, code);
  } catch {
    // Без сохранения язык выберется заново при перезагрузке.
  }
  applyStaticTexts();
}

export async function initI18n() {
  await loadDictionary(FALLBACK);
  await setLanguage(detectLanguage());
}
