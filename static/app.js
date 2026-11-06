// Страница управления несколькими матрицами. Протокол — в protocol.js, тексты — в lang/*.json
// через i18n.js, здесь интерфейс и хранение. Все матрицы и их состояние — в localStorage.

import {
  AUDIO_MODES,
  EDID_CODES,
  edidUrl,
  cidrHosts,
  DEFAULT_PORT,
  networkBody,
  networkUrl,
  parseHost,
  parseState,
  revisionUrl,
  routeUrl,
  stateUrl,
} from "./protocol.js";
import { applyStaticTexts, currentLanguage, initI18n, LANGUAGES, setLanguage, t } from "./i18n.js";

const STORE_KEY = "hdmi-matrix.v1";
// Настройки отображения на сервере: адрес обслуживает hdmi-ui, версия формата совпадает с config.go.
const CONFIG_URL = "/api/config";
const IO_CONFIG_VERSION = 1;
const IO_CONFIG_TIMEOUT_MS = 3000;
const POLL_MS = 3000;
const REQUEST_TIMEOUT_MS = 6000;
// Для проверки доступности таймаут короче: офлайн-матрица не должна держать опрос дольше секунды.
const PRESENCE_TIMEOUT_MS = 1500;
const INPUTS = [1, 2, 3, 4, 5, 6, 7, 8];
const OUTPUTS = [1, 2, 3, 4, 5, 6, 7, 8];
const FLAG_KEYS = ["hdr", "threeD", "bit30", "bit36", "bit48"];
// Копии TV: код EDID → номер экрана. Коды 5–9 и A–C, см. protocol.js.
const EDID_COPY_SOURCE = { 5: 1, 6: 2, 7: 3, 8: 4, 9: 5, A: 6, B: 7, C: 8 };
// Палитра точек у тегов. Цвет берётся из текста тега (tagColor), поэтому одинаковые теги всегда одного цвета.
const IO_COLORS = [
  "#7aa2f7", "#7dcfff", "#bb9af7", "#9ece6a", "#e0af68", "#ff9e64", "#f7768e", "#73daca",
  "#2ac3de", "#b4f9f8", "#c3e88d", "#ffc777", "#ff007c", "#c099ff", "#89ddff", "#d7827e",
];
// Лимиты держат матрицу читаемой: длинная подпись уже не влезает в колонку 8×8.
const IO_LABEL_MAX = 32;
const IO_TAG_MAX = 16;
const IO_MAX_TAGS = 6;

const $ = (id) => document.getElementById(id);

// ── Хранилище ────────────────────────────────────────────────────────────────────
// Матрицы ключуются по MAC: он постоянный, а IP может смениться. Состояние интерфейса
// (раскрытые блоки) хранится рядом, маршрутизация и EDID — никогда: их читаем с устройства.

function emptyStore() {
  return { active: null, devices: {} };
}

function loadStore() {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORE_KEY));
    return parsed && typeof parsed.devices === "object" ? parsed : emptyStore();
  } catch {
    return emptyStore(); // localStorage может быть недоступен: работаем без сохранения
  }
}

function saveStore() {
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(store));
  } catch {
    // Без сохранения интерфейс всё равно работает, просто состояние не переживёт перезагрузку.
  }
}

const store = loadStore();

// ── Состояние рантайма ───────────────────────────────────────────────────────────

const runtime = {
  live: null,            // последнее состояние активной матрицы
  busy: false,           // идёт переключение: опрос не должен затирать подсветку
  edidDirty: new Set(),  // входы, где EDID правят прямо сейчас
  netDirty: false,
  renderedMac: null,     // для какой матрицы построены строки EDID
  online: {},            // доступность по MAC: true/false; нет ключа — ещё не проверяли
  link: { ok: false, key: "link.nodata" },
  view: "loading",       // loading | ready | stale | offline
};

function activeDevice() {
  return store.active ? store.devices[store.active] ?? null : null;
}

// ── Запросы к матрице ────────────────────────────────────────────────────────────
// Единая точка входа для всех обращений к устройству. Очередь — на каждую матрицу отдельно:
// зависшая или выключенная матрица не задерживает запросы к остальным.

const deviceQueues = new Map();
function queueDevice(ip, task) {
  const previous = deviceQueues.get(ip) ?? Promise.resolve();
  const run = previous.then(task, task);
  deviceQueues.set(ip, run.catch(() => {}));
  return run;
}

// Сессия истекла или её не было: страница уходит на вход, а не висит в состоянии «нет связи».
function checkSession(response) {
  if (response.status === 401) {
    location.assign("login");
    throw new Error("нужен вход");
  }
  return response;
}

function deviceRequest(ip, url, { method = "GET", body, headers, timeout = REQUEST_TIMEOUT_MS, queued = true } = {}) {
  const run = () => fetch(url, { method, body, headers, cache: "no-store", signal: AbortSignal.timeout(timeout) }).then(checkSession);
  return queued ? queueDevice(ip, run) : run();
}

// Команды записи: ответ не важен, матрица часто отвечает пустым или рвёт соединение (прокси вернёт 502).
// Правду показывает только последующее чтение состояния.
function sendCommand(ip, url, options) {
  return deviceRequest(ip, url, options).catch(() => null);
}

async function readState(ip) {
  const response = await deviceRequest(ip, stateUrl(ip));
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return parseState(await response.text());
}

// Проверка, что по адресу отвечает именно матрица: AutoGetAllData разбирается в 22 поля,
// AutoGetRevID отдаёт код ревизии вида «52=…».
async function probeMatrix(ip) {
  return queueDevice(ip, async () => {
    const [stateResponse, revisionResponse] = await Promise.all([
      deviceRequest(ip, stateUrl(ip), { queued: false }),
      deviceRequest(ip, revisionUrl(ip), { queued: false }),
    ]);
    if (!stateResponse.ok) throw new Error(t("err.noResponse", { ip, status: stateResponse.status }));
    const state = parseState(await stateResponse.text());
    if (!(await revisionResponse.text()).startsWith("52=")) throw new Error(t("err.notMatrix"));
    return state;
  });
}

// ── Визуал: связь, состояние просмотра, ошибки ───────────────────────────────────

function setLink(ok, key) {
  runtime.link = { ok, key };
  renderLink();
}

function renderLink() {
  $("linkPill").className = `pill ${runtime.link.ok ? "ok" : "bad"}`;
  $("linkText").textContent = t(runtime.link.key);
}

// Состояние просмотра: loading — данных ещё нет, offline — нет связи и данных нет,
// stale — связи нет, но последние данные показываем приглушённо, ready — всё актуально.
const VIEW_TEXT = { loading: "loader.loading", offline: "link.bad", stale: "link.bad", ready: null };

function setViewState(view) {
  runtime.view = view;
  $("workspace").dataset.state = view;
  renderViewState();
}

function renderViewState() {
  const key = VIEW_TEXT[runtime.view];
  for (const element of document.querySelectorAll(".loader-text")) element.textContent = key ? t(key) : "";
  // Указать новый адрес имеет смысл только когда матрица недоступна.
  for (const button of document.querySelectorAll(".loader-action")) button.hidden = runtime.view !== "offline";
}

function showError(id, message) {
  $(id).textContent = message ?? "";
}

// ── Добавление, смена адреса и удаление ──────────────────────────────────────────

// Режим «новый адрес» для уже добавленной матрицы: MAC — единственная связь с записью,
// поэтому адрес принимаем только если отвечает матрица с тем же MAC.
let addModeMac = null;

function renderAddTitles() {
  const device = addModeMac ? store.devices[addModeMac] : null;
  $("addTitle").textContent = device
    ? t("welcome.titleEdit", { name: device.name || device.ip })
    : t("welcome.title");
  $("addSubmit").textContent = device ? t("welcome.submitEdit") : t("welcome.submit");
}

function startEditAddress() {
  const device = activeDevice();
  if (!device) return;
  addModeMac = store.active;
  $("addInput").value = device.ip;
  renderAddTitles();
  setWelcome(true);
  $("addInput").focus();
}

function resetAddMode() {
  addModeMac = null;
  renderAddTitles();
}

function setWelcome(visible) {
  $("welcome").hidden = !visible;
  $("workspace").hidden = visible;
}

function showAddPanel() {
  setWelcome(true);
  $("emptyHint").hidden = Object.keys(store.devices).length > 0;
  $("addInput").focus();
}

async function addDevice(event) {
  event.preventDefault();
  // Адрес может содержать порт: "192.168.1.50" или "192.168.1.50:8080". По умолчанию 80.
  const parsed = parseHost($("addInput").value);
  showError("addError", "");
  if (parsed.error) {
    showError("addError", t(parsed.error === "port" ? "err.port" : "err.ip"));
    return;
  }
  const ip = parsed.host;
  const button = $("addForm").querySelector("button");
  button.disabled = true;
  try {
    const state = await probeMatrix(ip);
    if (addModeMac) {
      if (state.mac !== addModeMac) {
        showError("addError", t("err.otherMatrix", { got: state.mac, need: addModeMac }));
        return;
      }
      store.devices[addModeMac].ip = ip;
      saveStore();
      const mac = addModeMac;
      resetAddMode();
      $("addInput").value = "";
      await activate(mac);
      return;
    }
    const existing = store.devices[state.mac] ?? { ui: { edidOpen: [] } };
    store.devices[state.mac] = { ...existing, ip, name: state.name };
    $("addInput").value = "";
    await activate(state.mac);
  } catch (error) {
    showError("addError", t("err.add", { msg: error.message }));
  } finally {
    button.disabled = false;
  }
}

// Подтверждение в стеклянном попапе вместо системного окна. Резолвится true/false.
function askConfirm({ title, text, okLabel }) {
  const modal = $("confirmModal");
  $("confirmTitle").textContent = title;
  $("confirmText").textContent = text;
  $("confirmOk").textContent = okLabel;
  modal.hidden = false;
  // Фокус на «Отмена»: случайный Enter не должен удалить матрицу.
  $("confirmCancel").focus();

  return new Promise((resolve) => {
    const finish = (answer) => {
      modal.hidden = true;
      $("confirmOk").removeEventListener("click", onOk);
      $("confirmCancel").removeEventListener("click", onCancel);
      modal.removeEventListener("click", onBackdrop);
      document.removeEventListener("keydown", onKey);
      resolve(answer);
    };
    const onOk = () => finish(true);
    const onCancel = () => finish(false);
    const onBackdrop = (event) => {
      if (event.target === modal) finish(false);
    };
    const onKey = (event) => {
      if (event.key === "Escape") finish(false);
    };
    $("confirmOk").addEventListener("click", onOk);
    $("confirmCancel").addEventListener("click", onCancel);
    modal.addEventListener("click", onBackdrop);
    document.addEventListener("keydown", onKey);
  });
}

async function removeActive() {
  const device = activeDevice();
  if (!device) return;
  const confirmed = await askConfirm({
    title: t("confirm.remove.title"),
    text: t("confirm.remove.text", { name: device.name || device.ip }),
    okLabel: t("confirm.remove.ok"),
  });
  if (!confirmed) return;
  delete store.devices[store.active];
  const next = Object.keys(store.devices)[0] ?? null;
  store.active = next;
  saveStore();
  runtime.live = null;
  renderTabs();
  if (next) activate(next);
  else showAddPanel();
}

// ── Вкладки матриц ───────────────────────────────────────────────────────────────

function renderTabs() {
  const tabs = $("tabs");
  tabs.replaceChildren();
  for (const [mac, device] of Object.entries(store.devices)) {
    const tab = document.createElement("button");
    tab.type = "button";
    const status = runtime.online[mac];
    const link = status === undefined ? "" : status ? "online" : "offline";
    tab.className = `tab ${mac === store.active ? "on" : ""} ${link}`;
    tab.textContent = device.name || device.ip;
    tab.title = `${device.ip} · ${mac}`;
    tab.addEventListener("click", () => activate(mac));
    tabs.append(tab);
  }
  const add = document.createElement("button");
  add.type = "button";
  add.className = "tab tab-add";
  add.textContent = t("tabs.add");
  add.addEventListener("click", () => {
    store.active = null;
    saveStore();
    runtime.live = null;
    resetAddMode();
    showAddPanel();
  });
  tabs.append(add);
}

function setOnline(mac, online) {
  if (runtime.online[mac] === online) return;
  runtime.online[mac] = online;
  renderTabs();
}

async function activate(mac) {
  if (!store.devices[mac]) return;
  resetAddMode();
  store.active = mac;
  saveStore();
  runtime.live = null;
  runtime.edidDirty.clear();
  runtime.netDirty = false;
  runtime.renderedMac = null;
  // Форма сети одна на все матрицы: без сброса на офлайн-матрице остались бы значения прошлой.
  $("netForm").reset();
  $("edidList").replaceChildren();
  // Сразу очищаем вид: иначе на экране остаются «зелёные» ячейки прошлой матрицы.
  setViewState("loading");
  showError("routeError", "");
  renderIoLabels();
  renderMatrix();
  setWelcome(false);
  renderTabs();
  setLink(false, "link.connecting");
  await refresh();
}

// ── Подписи и теги входов и выходов ──────────────────────────────────────────────
// Хранятся в записи матрицы (по MAC) вместе с адресом, поэтому переживают смену IP.
// Пустая подпись значит «как по умолчанию»: в данных ничего не пишем.

function ioEntry(device, kind, n, create = false) {
  if (!device) return null;
  device.io ??= { in: {}, out: {} };
  const entries = device.io[kind];
  if (!entries[n] && create) entries[n] = { label: "", tags: [] };
  return entries[n] ?? null;
}

function ioDefaultName(kind, n) {
  return t(kind === "in" ? "matrix.in" : "matrix.out", { n });
}

// Подпись там, где нужно короткое имя (строки EDID): без подписи остаётся «IN n».
function ioName(kind, n) {
  return ioEntry(activeDevice(), kind, n)?.label || ioDefaultName(kind, n);
}

// Цвет тега — функция от его текста: одинаковый тег всегда одного цвета, после перезагрузки тоже.
// FNV-1a — простой хеш без зависимостей. Регистр и пробелы по краям не различаем: «HDR» и «hdr» — один тег.
function tagColor(text) {
  let hash = 0x811c9dc5;
  for (const char of text.trim().toLowerCase()) {
    hash ^= char.codePointAt(0);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return IO_COLORS[hash % IO_COLORS.length];
}

// Один тег: точка цвета и текст. В матрице компактная версия, в редакторе — с кнопкой удаления.
function tagChip(tag, variant) {
  const chip = el("span", `io-tag io-tag-${variant}`);
  const dot = el("i", "io-dot");
  dot.style.background = tagColor(tag.text);
  chip.append(dot, el("span", "io-tag-text", tag.text));
  return chip;
}

// Заголовок или строка выхода: «IN n» всегда, подпись второй строкой, теги под ней.
function fillIoCell(cell, kind, n) {
  const entry = ioEntry(activeDevice(), kind, n);
  const lines = [el("span", "io-num", ioDefaultName(kind, n))];
  if (entry?.label) lines.push(el("span", "io-name", entry.label));
  if (entry?.tags.length) {
    const line = el("span", "io-tags");
    for (const tag of entry.tags) line.append(tagChip(tag, "mini"));
    lines.push(line);
    cell.title = entry.tags.map((tag) => tag.text).join(", ");
  } else {
    cell.removeAttribute("title");
  }
  cell.replaceChildren(...lines);
}

// Подписи, которые видны вне матрицы: заголовки и строки матрицы, имена входов в EDID.
function renderIoLabels() {
  renderMatrixLabels();
  $("ioMenuEdit").textContent = t("io.edit");
  for (const row of document.querySelectorAll(".edid-row")) renderEdidRowLabels(row);
}

// ── Меню и редактор ──────────────────────────────────────────────────────────────
// Правый клик по заголовку открывает меню, его пункт — редактор. Редактор работает с черновиком:
// правки попадают в запись только после «Сохранить», Escape и «Отмена» их отбрасывают.

let ioTarget = null; // заголовок, для которого открыто меню
let ioDraft = null;  // черновик редактора: { kind, n, label, tags }

function openIoMenu(event, kind, n) {
  event.preventDefault();
  if (!activeDevice()) return;
  ioTarget = { kind, n };
  const menu = $("ioMenu");
  menu.hidden = false;
  // Меню не должно уезжать за край окна: пункт у правого или нижнего края стал бы недоступен.
  const left = Math.min(event.clientX, window.innerWidth - menu.offsetWidth - 8);
  const top = Math.min(event.clientY, window.innerHeight - menu.offsetHeight - 8);
  menu.style.left = `${Math.max(8, left)}px`;
  menu.style.top = `${Math.max(8, top)}px`;
}

function closeIoMenu() {
  $("ioMenu").hidden = true;
  ioTarget = null;
}

function openIoEditor(kind, n) {
  const device = activeDevice();
  if (!device) return;
  const entry = ioEntry(device, kind, n);
  ioDraft = { kind, n, label: entry?.label ?? "", tags: (entry?.tags ?? []).map((tag) => ({ ...tag })) };
  $("ioTitle").textContent = ioDefaultName(kind, n);
  $("ioLabel").value = ioDraft.label;
  $("ioLabel").placeholder = ioDefaultName(kind, n);
  $("ioTagInput").value = "";
  showError("ioError", "");
  renderIoDraftTags();
  $("ioModal").hidden = false;
  $("ioLabel").focus();
}

function closeIoEditor() {
  ioDraft = null;
  $("ioModal").hidden = true;
}

function renderIoDraftTags() {
  $("ioTags").replaceChildren(...ioDraft.tags.map((tag, index) => editableTag(index, tag)));
  const full = ioDraft.tags.length >= IO_MAX_TAGS;
  $("ioTagInput").disabled = full;
  $("ioTagAdd").disabled = full;
  $("ioTagInput").title = full ? t("io.limit", { max: IO_MAX_TAGS }) : "";
}

function editableTag(index, tag) {
  const chip = tagChip(tag, "full");
  const remove = el("button", "io-tag-x", "×");
  remove.type = "button";
  remove.setAttribute("aria-label", t("io.tagRemove", { tag: tag.text }));
  remove.addEventListener("click", () => {
    ioDraft.tags.splice(index, 1);
    renderIoDraftTags();
  });
  chip.append(remove);
  return chip;
}

function addDraftTag() {
  const input = $("ioTagInput");
  const text = input.value.trim();
  if (!text || ioDraft.tags.length >= IO_MAX_TAGS) return;
  ioDraft.tags.push({ text });
  input.value = "";
  renderIoDraftTags();
  input.focus();
}

function saveIoDraft(event) {
  event.preventDefault();
  const device = activeDevice();
  if (!device || !ioDraft) return;
  const entry = ioEntry(device, ioDraft.kind, ioDraft.n, true);
  entry.label = $("ioLabel").value.trim();
  entry.tags = ioDraft.tags;
  closeIoEditor();
  commitIo();
}

// Подписи и теги живут на сервере контейнера, чтобы их видели все браузеры. localStorage остаётся
// запасным вариантом: если сервер недоступен, работаем с тем, что уже есть в браузере.
async function loadIoConfig() {
  let remote;
  try {
    const response = checkSession(await fetch(CONFIG_URL, { cache: "no-store", signal: AbortSignal.timeout(IO_CONFIG_TIMEOUT_MS) }));
    if (!response.ok) return;
    remote = await response.json();
  } catch {
    return;
  }
  const remoteDevices = remote.devices ?? {};
  if (Object.keys(remoteDevices).length > 0) {
    for (const [mac, device] of Object.entries(store.devices)) {
      if (remoteDevices[mac]?.io) device.io = remoteDevices[mac].io;
    }
    saveStore();
  } else if (Object.values(store.devices).some((device) => device.io)) {
    // Первый запуск новой версии: подписи, накопленные в этом браузере, переезжают на сервер.
    pushIoConfig();
  }
}

// Отправляем подписи всех матриц целиком: объём мал, а частичные обновления усложнили бы сервер.
// Ошибку не показываем в интерфейсе: подписи остаются в localStorage и уйдут при следующем изменении.
async function pushIoConfig() {
  const devices = {};
  for (const [mac, device] of Object.entries(store.devices)) {
    if (device.io) devices[mac] = { io: device.io };
  }
  try {
    const response = checkSession(await fetch(CONFIG_URL, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ version: IO_CONFIG_VERSION, devices }),
      cache: "no-store",
      signal: AbortSignal.timeout(IO_CONFIG_TIMEOUT_MS),
    }));
    if (!response.ok) console.warn(`подписи не сохранились на сервере: HTTP ${response.status}`);
  } catch (error) {
    console.warn(`подписи не сохранились на сервере: ${error.message}`);
  }
}

// Любое изменение подписей: сохранить в браузере, на сервере и перерисовать то, что их показывает.
function commitIo() {
  saveStore();
  renderIoLabels();
  pushIoConfig();
}

// ── Матрица ──────────────────────────────────────────────────────────────────────

function buildMatrix() {
  const matrix = $("matrix");
  matrix.append(document.createElement("div"));
  for (const input of INPUTS) {
    const head = document.createElement("div");
    head.className = "head";
    head.dataset.n = input;
    // Меню и редактор подписи открываются правым кликом по заголовку; сами элементы живут весь сеанс.
    head.addEventListener("contextmenu", (event) => openIoMenu(event, "in", input));
    matrix.append(head);
  }
  for (const output of OUTPUTS) {
    const label = document.createElement("div");
    label.className = "out";
    label.dataset.n = output;
    label.addEventListener("contextmenu", (event) => openIoMenu(event, "out", output));
    matrix.append(label);
    for (const input of INPUTS) {
      const cell = document.createElement("button");
      cell.className = "cell";
      cell.type = "button";
      cell.textContent = input;
      cell.dataset.output = output;
      cell.dataset.input = input;
      cell.addEventListener("click", () => route(output, input));
      matrix.append(cell);
    }
  }

  const allRow = $("allRow");
  for (const input of INPUTS) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = `→${input}`;
    button.dataset.n = input;
    button.addEventListener("click", () => routeAll(input));
    allRow.append(button);
  }
}

// Подписи матрицы: вызывается при построении и при смене языка.
function renderMatrixLabels() {
  for (const element of document.querySelectorAll(".head")) fillIoCell(element, "in", Number(element.dataset.n));
  for (const element of document.querySelectorAll(".out")) fillIoCell(element, "out", Number(element.dataset.n));
  for (const cell of document.querySelectorAll(".cell")) {
    cell.setAttribute("aria-label", t("matrix.cell", { o: cell.dataset.output, i: cell.dataset.input }));
  }
  for (const button of document.querySelectorAll("#allRow button")) {
    button.title = t("matrix.allTo", { n: button.dataset.n });
  }
}

function renderMatrix() {
  const live = runtime.live;
  for (const cell of document.querySelectorAll(".cell")) {
    const output = Number(cell.dataset.output);
    const input = Number(cell.dataset.input);
    cell.classList.toggle("on", !!live && live.outputs[output - 1] === input);
    cell.disabled = runtime.busy;
  }
  for (const button of document.querySelectorAll("#allRow button")) button.disabled = runtime.busy;
}

function setBusy(value) {
  runtime.busy = value;
  renderMatrix();
}

async function route(output, input) {
  const device = activeDevice();
  if (runtime.busy || !runtime.live || !device) return;
  setBusy(true);
  showError("routeError", "");
  // Подсветка сразу: матрица отвечает медленно, а пустой ответ — норма.
  runtime.live.outputs[output - 1] = input;
  renderMatrix();
  try {
    await sendCommand(device.ip, routeUrl(device.ip, output, input));
    runtime.live = await readState(device.ip);
    if (runtime.live.outputs[output - 1] !== input) {
      showError("routeError", t("err.routeNotSwitched", { o: output, i: input }));
    }
  } catch (error) {
    showError("routeError", t("err.route", { msg: error.message }));
  } finally {
    setBusy(false);
    renderAll();
  }
}

async function routeAll(input) {
  const device = activeDevice();
  if (runtime.busy || !runtime.live || !device) return;
  setBusy(true);
  showError("routeError", "");
  try {
    // Выходы по одному: так устройство не путает команды.
    for (const output of OUTPUTS) await sendCommand(device.ip, routeUrl(device.ip, output, input));
    runtime.live = await readState(device.ip);
    const stuck = OUTPUTS.filter((output) => runtime.live.outputs[output - 1] !== input);
    if (stuck.length) showError("routeError", t("err.routeStuck", { list: stuck.join(", ") }));
  } catch (error) {
    showError("routeError", t("err.route", { msg: error.message }));
  } finally {
    setBusy(false);
    renderAll();
  }
}

// ── EDID ─────────────────────────────────────────────────────────────────────────

function edidName(code) {
  return /^[1-4]$/.test(code) ? t(`edid.code.${code}`) : t("edid.copy", { n: EDID_COPY_SOURCE[code] ?? code });
}

// Раскрытые блоки EDID запоминаем на матрицу: после перезагрузки страница откроется так же.
function rememberOpenRows() {
  const device = activeDevice();
  if (!device) return;
  device.ui.edidOpen = [...document.querySelectorAll(".edid-row[open]")].map((row) => Number(row.dataset.input));
  saveStore();
}

// Флаг языка — SVG-файл: эмодзи-флаги на Windows выглядят как пара букв.
function flagImage(code) {
  const img = el("img", "flag-img");
  img.src = `flags/${code}.svg`;
  img.alt = "";
  img.width = 22;
  img.height = 15;
  return img;
}

function el(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

function buildEdidRow(input) {
  const row = el("details", "edid-row");
  row.dataset.input = input;
  row.addEventListener("toggle", rememberOpenRows);

  const summary = el("summary");
  const label = el("span", "in");
  label.dataset.inLabel = input;
  const summaryText = el("span", "sum");
  summaryText.dataset.summary = "";
  summary.append(label, summaryText);

  const body = el("div", "edid-body");
  const tags = el("div", "flags");
  tags.dataset.tags = "";

  const select = el("select");
  select.dataset.code = "";
  for (const code of EDID_CODES) {
    const option = el("option");
    option.value = code;
    select.append(option);
  }

  const audio = el("select");
  audio.dataset.audio = "";
  for (const mode of AUDIO_MODES) {
    const option = el("option");
    option.value = mode;
    audio.append(option);
  }

  const toggles = el("div", "toggles");
  for (const key of FLAG_KEYS) {
    const tag = el("label", "tag");
    const box = el("input");
    box.type = "checkbox";
    box.dataset.flag = key;
    const text = el("span");
    text.dataset.flagLabel = key;
    tag.append(box, " ", text);
    toggles.append(tag);
  }

  const apply = el("button", "btn btn-apply");
  apply.type = "button";
  apply.dataset.apply = "";
  apply.addEventListener("click", () => applyEdid(input, row));

  // Редактирование спрятано в отдельный блок; оранжевое у его заголовка видно и свёрнутым.
  const edit = el("details", "edid-edit");
  const editSummary = el("summary");
  editSummary.dataset.editSummary = "";
  const editGrid = el("div", "edit-grid");
  editGrid.append(select, audio, toggles, apply);
  edit.append(editSummary, editGrid);

  // Любое касание контрола помечает строку как редактируемую: опрос её не перетрёт.
  for (const control of [select, audio, toggles]) {
    const onEdit = () => {
      runtime.edidDirty.add(input);
      updateDirty(row);
    };
    control.addEventListener("change", onEdit);
    control.addEventListener("input", onEdit);
  }

  body.append(tags, edit);
  row.append(summary, body);
  return row;
}

// Подписи строки EDID: вызывается при построении и при смене языка.
function renderEdidRowLabels(row) {
  row.querySelector("[data-in-label]").textContent = ioName("in", Number(row.dataset.input));
  for (const option of row.querySelectorAll("[data-code] option")) option.textContent = edidName(option.value);
  for (const option of row.querySelectorAll("[data-audio] option")) {
    option.textContent = t("edid.audio", { mode: option.value });
  }
  for (const label of row.querySelectorAll("[data-flag-label]")) label.textContent = t(`flag.${label.dataset.flagLabel}`);
  row.querySelector("[data-apply]").textContent = t("edid.apply");
  row.querySelector("[data-edit-summary]").textContent = t("edid.edit");
  if (row._saved) fillEdidRow(row, row._saved);
}

// Строки EDID строятся заново только при смене матрицы, иначе пропадает их состояние.
function ensureEdidRows(mac) {
  const list = $("edidList");
  if (runtime.renderedMac === mac && list.children.length) return;
  list.replaceChildren();
  const openInputs = new Set(activeDevice()?.ui.edidOpen ?? []);
  for (const input of INPUTS) {
    const row = buildEdidRow(input);
    if (openInputs.has(input)) row.open = true;
    list.append(row);
    renderEdidRowLabels(row);
  }
  runtime.renderedMac = mac;
}

// Сохранённое на матрице состояние строки хранится отдельно от контролов:
// по нему подсвечиваются несохранённые изменения.
function fillEdidRow(row, edid) {
  row._saved = edid;

  const summaryText = row.querySelector("[data-summary]");
  summaryText.replaceChildren();
  // Имя — жирным, остальное берём из шаблона языка: «{name} · audio {audio}» начинается с имени.
  summaryText.append(el("b", null, edidName(edid.code)), t("edid.summary", { name: "", audio: edid.audio }));

  const tags = row.querySelector("[data-tags]");
  tags.replaceChildren();
  for (const key of FLAG_KEYS) {
    // Плашка — не чекбокс: атрибут отдельный, иначе её примут за контрол при сборе данных.
    const tag = el("span", `tag ${edid[key] ? "on" : ""}`, t(`flag.${key}`));
    tag.dataset.tag = key;
    tags.append(tag);
  }

  // Контролы трогаем только когда пользователь не правит строку, иначе сотрём его выбор.
  if (!runtime.edidDirty.has(Number(row.dataset.input))) {
    row.querySelector("[data-code]").value = edid.code;
    row.querySelector("[data-audio]").value = edid.audio;
    for (const box of row.querySelectorAll("[data-flag]")) box.checked = edid[box.dataset.flag];
  }
  updateDirty(row);
}

// Оранжевым отмечаются изменённые флаги в верхней строке, контролы и кнопка «Применить».
// Когда различий нет, строка снова становится «чистой».
function updateDirty(row) {
  const saved = row._saved;
  if (!saved) return;
  const code = row.querySelector("[data-code]");
  const audio = row.querySelector("[data-audio]");
  const codeChanged = code.value !== saved.code;
  const audioChanged = audio.value !== saved.audio;
  code.classList.toggle("changed", codeChanged);
  audio.classList.toggle("changed", audioChanged);

  let anyChange = codeChanged || audioChanged;
  for (const box of row.querySelectorAll("[data-flag]")) {
    const key = box.dataset.flag;
    const changed = box.checked !== saved[key];
    anyChange = anyChange || changed;
    row.querySelector(`[data-tags] [data-tag="${key}"]`).classList.toggle("changed", changed);
  }

  row.querySelector("[data-apply]").classList.toggle("pending", anyChange);
  row.querySelector(".edid-edit > summary").classList.toggle("pending", anyChange);
  if (!anyChange) runtime.edidDirty.delete(Number(row.dataset.input));
}

function renderEdid() {
  const live = runtime.live;
  if (!live || !activeDevice()) return;
  ensureEdidRows(store.active);
  live.edid.forEach((edid, index) => fillEdidRow($("edidList").children[index], edid));
}

async function applyEdid(input, row) {
  const device = activeDevice();
  if (!device) return;
  const edid = {
    code: row.querySelector("[data-code]").value,
    audio: row.querySelector("[data-audio]").value,
  };
  for (const box of row.querySelectorAll("[data-flag]")) edid[box.dataset.flag] = box.checked;

  const button = row.querySelector("[data-apply]");
  button.disabled = true;
  showError("edidError", "");
  try {
    await sendCommand(device.ip, edidUrl(device.ip, input, edid));
    runtime.live = await readState(device.ip);
    const actual = runtime.live.edid[input - 1];
    if (actual.code !== edid.code || actual.audio !== edid.audio) {
      showError("edidError", t("err.edidNotApplied", { in: input }));
    } else {
      runtime.edidDirty.delete(input);
    }
  } catch (error) {
    showError("edidError", t("err.edid", { in: input, msg: error.message }));
  } finally {
    button.disabled = false;
    renderAll();
  }
}

// ── Сеть ─────────────────────────────────────────────────────────────────────────

// Поля через elements: поле с именем "name" перекрывает свойство form.name.
function renderNetwork() {
  const live = runtime.live;
  if (!live || runtime.netDirty) return;
  const fields = $("netForm").elements;
  fields.namedItem("name").value = live.name;
  fields.namedItem("ip").value = live.ip;
  fields.namedItem("mask").value = live.mask;
  fields.namedItem("gateway").value = live.gateway;
  fields.namedItem("dhcp").checked = live.dhcp;
  for (const key of ["ip", "mask", "gateway"]) fields.namedItem(key).disabled = live.dhcp;
  $("macText").textContent = t("net.mac", { mac: live.mac });
}

async function saveNetwork(event) {
  event.preventDefault();
  const device = activeDevice();
  if (!device) return;
  const fields = event.currentTarget.elements;
  const payload = {
    name: fields.namedItem("name").value.trim(),
    ip: fields.namedItem("ip").value.trim(),
    mask: fields.namedItem("mask").value.trim(),
    gateway: fields.namedItem("gateway").value.trim(),
    dhcp: fields.namedItem("dhcp").checked,
  };
  const confirmed = await askConfirm({
    title: payload.dhcp ? t("confirm.dhcp.title") : t("confirm.ip.title", { ip: payload.ip }),
    text: payload.dhcp ? t("confirm.dhcp.text") : t("confirm.ip.text"),
    okLabel: t("confirm.save"),
  });
  if (!confirmed) return;

  const button = event.currentTarget.querySelector("button");
  button.disabled = true;
  showError("netError", "");
  try {
    // Отправляем по текущему адресу: новый пока не работает. Ответа устройство обычно не даёт.
    await sendCommand(device.ip, networkUrl(device.ip), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: networkBody(payload),
    });
    runtime.netDirty = false;
    await followNewAddress(device.mac, payload.dhcp ? null : payload.ip);
  } catch (error) {
    showError("netError", t("err.net", { msg: error.message }));
  } finally {
    button.disabled = false;
  }
}

// После смены адреса матрица переезжает. Пробуем новый адрес несколько раз: устройству
// нужно время перезапустить сеть. Если не получилось, адрес остаётся в списке как есть.
async function followNewAddress(mac, newIp) {
  const device = store.devices[mac];
  if (!device) return;
  if (!newIp) {
    showError("netError", t("net.dhcpNote"));
    return;
  }
  for (let attempt = 0; attempt < 5; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 2500));
    try {
      const state = await probeMatrix(newIp);
      if (state.mac === mac) {
        device.ip = newIp;
        device.name = state.name;
        saveStore();
        renderTabs();
        showError("netError", t("net.saved", { ip: newIp }));
        return;
      }
    } catch {
      // ещё не поднялась: пробуем дальше
    }
  }
  showError("netError", t("net.savedNoReply", { ip: newIp }));
}

// ── Опрос ────────────────────────────────────────────────────────────────────────

function renderAll() {
  const live = runtime.live;
  const device = activeDevice();
  if (live && device) {
    $("deviceName").textContent = live.name || device.name || t("app.defaultName");
    $("deviceAddr").textContent = `${live.ip} · ${live.mask} · gw ${live.gateway}`;
  }
  renderMatrix();
  renderEdid();
  renderNetwork();
}

// Опрос шёл к той матрице, которая была активна в момент запуска. Если пользователь
// уже переключился или сменил адрес, ответ относится к прошлой записи и его нельзя применять.
function isStaleResponse(requestedMac, requestedIp) {
  return store.active !== requestedMac || store.devices[requestedMac]?.ip !== requestedIp;
}

async function refresh() {
  const device = activeDevice();
  if (!device || runtime.busy) return;
  const requestedMac = store.active;
  const requestedIp = device.ip;
  try {
    const state = await readState(requestedIp);
    if (isStaleResponse(requestedMac, requestedIp)) return;
    runtime.live = state;
    setLink(true, "link.ok");
    setOnline(requestedMac, true);
    setViewState("ready");
    renderAll();
  } catch {
    if (isStaleResponse(requestedMac, requestedIp)) return;
    setLink(false, "link.bad");
    setOnline(requestedMac, false);
    // Сырую ошибку (HTTP 502) не выводим: опрос идёт часто, состояние уже говорит всё.
    setViewState(runtime.live ? "stale" : "offline");
  }
}

// Доступность проверяем у всех матриц, а не только у активной: иначе вкладки неактивных
// никогда не узнают, что устройство пропало. Берём AutoGetRevID — самый короткий ответ.
// Проверки идут параллельно и мимо очереди: это только чтение, и медленная матрица
// не должна задерживать остальные.
async function checkPresence() {
  await Promise.all(
    Object.entries(store.devices).map(async ([mac, device]) => {
      let online = false;
      try {
        const response = await deviceRequest(device.ip, revisionUrl(device.ip), {
          queued: false,
          timeout: PRESENCE_TIMEOUT_MS,
        });
        online = response.ok && (await response.text()).startsWith("52=");
      } catch {
        online = false;
      }
      // Если адрес сменили, пока шла проверка, результат относится к старому адресу.
      if (store.devices[mac]?.ip !== device.ip) return;
      setOnline(mac, online);
    }),
  );
}

// ── Поиск матриц в сети ──────────────────────────────────────────────────────────
// Опрос только чтением (AutoGetRevID, затем AutoGetAllData), параллельно и с коротким таймаутом.
// Найденная матрица попадает в список только по кнопке «Добавить».

// Браузер держит не больше шести соединений на один адрес, поэтому параллельность меньше:
// запросы к пустым адресам не должны занимать слоты и отодвигать таймаут живой матрицы.
const SCAN_CONCURRENCY = 5;
const SCAN_TIMEOUT_MS = 2500;
const SCAN_ERRORS = { badRange: "scan.errBadRange", notPrivate: "scan.errNotPrivate", tooLarge: "scan.errTooLarge" };
let scanAbort = false;

// Проверка одного адреса: сначала короткий AutoGetRevID, полный конфиг только для матриц.
async function probeHost(host) {
  const revision = await deviceRequest(host, revisionUrl(host), { queued: false, timeout: SCAN_TIMEOUT_MS });
  if (!revision.ok || !(await revision.text()).startsWith("52=")) return null;
  const response = await deviceRequest(host, stateUrl(host), { queued: false });
  if (!response.ok) return null;
  return parseState(await response.text());
}

function setScanRunning(running) {
  $("scanStart").hidden = running;
  $("scanStop").hidden = !running;
  $("scanRange").disabled = running;
  $("scanPort").disabled = running;
}

function addScanResult(host, state) {
  const known = store.devices[state.mac];
  const item = el("li", "scan-item");
  const info = el("div", "scan-info");
  info.append(el("b", null, state.name || host), el("span", "scan-meta", `${host} · ${state.mac}`));
  const badge = el("span", `scan-badge ${known ? "known" : "new"}`, t(known ? "scan.known" : "scan.new"));
  const add = el("button", "btn btn-primary", t("scan.add"));
  add.type = "button";
  add.addEventListener("click", () => addFoundDevice(host, state));
  item.append(info, badge, add);
  $("scanResults").append(item);
}

// Уже известная матрица (тот же MAC) обновляет свой адрес, новая добавляется.
async function addFoundDevice(host, state) {
  const existing = store.devices[state.mac] ?? { ui: { edidOpen: [] } };
  store.devices[state.mac] = { ...existing, ip: host, name: state.name };
  saveStore();
  await activate(state.mac);
}

async function startScan(event) {
  event.preventDefault();
  showError("scanError", "");
  const range = cidrHosts($("scanRange").value);
  if (range.error) {
    showError("scanError", t(SCAN_ERRORS[range.error]));
    return;
  }
  const port = Number($("scanPort").value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    showError("scanError", t("err.port"));
    return;
  }
  const hosts = range.hosts.map((ip) => (port === DEFAULT_PORT ? ip : `${ip}:${port}`));

  scanAbort = false;
  $("scanResults").replaceChildren();
  setScanRunning(true);
  let done = 0;
  let found = 0;
  let next = 0;
  const updateStatus = () => {
    $("scanStatus").textContent = t("scan.progress", { done, total: hosts.length, found });
  };
  updateStatus();

  // Несколько рабочих тянут адреса из общего счётчика: ограничиваем параллельность.
  const worker = async () => {
    while (!scanAbort && next < hosts.length) {
      const host = hosts[next];
      next += 1;
      let state = null;
      try {
        state = await probeHost(host);
      } catch {
        state = null; // таймаут или не матрица: просто пропускаем адрес
      }
      done += 1;
      if (state) {
        found += 1;
        addScanResult(host, state);
      }
      updateStatus();
    }
  };
  await Promise.all(Array.from({ length: SCAN_CONCURRENCY }, worker));

  setScanRunning(false);
  if (!scanAbort) $("scanStatus").textContent = t("scan.done", { found });
  if (!found && !scanAbort) showError("scanError", t("scan.none"));
}

// ── Язык и часы ──────────────────────────────────────────────────────────────────

function renderLangButton() {
  const language = currentLanguage();
  $("langFlag").replaceChildren(flagImage(language.code));
  $("langCode").textContent = language.code.toUpperCase();
  for (const item of document.querySelectorAll(".lang-item")) {
    item.classList.toggle("on", item.dataset.code === language.code);
    item.setAttribute("aria-selected", String(item.dataset.code === language.code));
  }
}

function buildLangMenu() {
  const menu = $("langMenu");
  for (const language of LANGUAGES) {
    const item = el("button", "lang-item");
    item.type = "button";
    item.role = "option";
    item.dataset.code = language.code;
    item.append(el("span", "lang-flag", ""), el("span", "lang-name", language.name), el("span", "lang-code", language.code.toUpperCase()));
    item.querySelector(".lang-flag").replaceChildren(flagImage(language.code));
    item.addEventListener("click", () => chooseLanguage(language.code));
    menu.append(item);
  }
}

function setLangMenu(open) {
  $("langMenu").hidden = !open;
  $("langBtn").setAttribute("aria-expanded", String(open));
}

async function chooseLanguage(code) {
  setLangMenu(false);
  await setLanguage(code);
  renderAllLabels();
}

// Все подписи, которые строятся из JS: при смене языка пересобираем их по тем же функциям.
function renderAllLabels() {
  renderLangButton();
  renderLink();
  renderViewState();
  renderAddTitles();
  renderIoLabels();
  renderTabs();
  renderAll();
  tickClock();
}

function tickClock() {
  $("clock").textContent = new Date().toLocaleTimeString(currentLanguage().locale);
}

// Меню подписи закрываем кликом мимо, Escape, прокруткой и изменением окна: иначе оно повисает.
// Редактор закрывается Escape и кликом по затемнению; Enter в поле тега добавляет тег, а не сохраняет.
function bindIoEditor() {
  $("ioMenuEdit").addEventListener("click", () => {
    const target = ioTarget;
    closeIoMenu();
    if (target) openIoEditor(target.kind, target.n);
  });
  // pointerdown, а не click: после правого клика браузер может прислать click по заголовку,
  // и меню закрылось бы сразу же после открытия. pointerdown приходит раньше contextmenu.
  document.addEventListener("pointerdown", (event) => {
    if (!$("ioMenu").contains(event.target)) closeIoMenu();
  });
  window.addEventListener("resize", closeIoMenu);
  window.addEventListener("scroll", closeIoMenu, true);
  $("ioForm").addEventListener("submit", saveIoDraft);
  $("ioCancel").addEventListener("click", closeIoEditor);
  $("ioModal").addEventListener("click", (event) => {
    if (event.target === $("ioModal")) closeIoEditor();
  });
  $("ioTagAdd").addEventListener("click", addDraftTag);
  $("ioTagInput").addEventListener("keydown", (event) => {
    if (event.key !== "Enter") return;
    event.preventDefault();
    addDraftTag();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    closeIoMenu();
    if (!$("ioModal").hidden) closeIoEditor();
  });
}

// Панели EDID и «Сеть» закрыты по умолчанию. Состояние запоминаем отдельно для каждой,
// на весь браузер: раскладка одна для всех матриц, а не привязана к конкретной.
function bindCollapsiblePanels() {
  for (const [id, key] of [["edidPanel", "edid"], ["netPanel", "net"]]) {
    const panel = $(id);
    panel.open = store.panels?.[key] === true;
    panel.addEventListener("toggle", () => {
      store.panels = { ...store.panels, [key]: panel.open };
      saveStore();
    });
  }
}

function bindLanguageMenu() {
  $("langBtn").addEventListener("click", (event) => {
    event.stopPropagation();
    setLangMenu($("langMenu").hidden);
  });
  document.addEventListener("click", (event) => {
    if (!$("langMenu").contains(event.target)) setLangMenu(false);
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") setLangMenu(false);
  });
}

// ── Старт ────────────────────────────────────────────────────────────────────────

async function init() {
  await initI18n();
  buildMatrix();
  buildLangMenu();
  bindLanguageMenu();
  bindIoEditor();
  bindCollapsiblePanels();
  renderLangButton();
  renderIoLabels();

  $("addForm").addEventListener("submit", addDevice);
  for (const button of document.querySelectorAll(".loader-action")) button.addEventListener("click", startEditAddress);
  $("removeBtn").addEventListener("click", removeActive);
  $("scanForm").addEventListener("submit", startScan);
  $("scanStop").addEventListener("click", () => { scanAbort = true; });
  $("netForm").addEventListener("submit", saveNetwork);
  $("netForm").addEventListener("input", () => { runtime.netDirty = true; });

  setLink(false, "link.nodata");
  setInterval(tickClock, 1000);
  tickClock();
  renderTabs();
  // Опрос запущен всегда: матрицу могут добавить уже после загрузки страницы.
  setInterval(refresh, POLL_MS);
  setInterval(checkPresence, POLL_MS);

  // Подписи подгружаем до первой активации: иначе матрица сначала покажет «IN 1» без подписей.
  await loadIoConfig();
  renderIoLabels();

  const devices = Object.keys(store.devices);
  if (!devices.length) {
    showAddPanel();
    return;
  }
  activate(store.active && store.devices[store.active] ? store.active : devices[0]);
}

init();
