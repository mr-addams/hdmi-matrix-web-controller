// Протокол матрицы в том виде, как его использует штатный web-UI.
// Модуль без DOM: его можно проверить в Node отдельно от страницы.
//
// Каждая матрица адресуется по своему IP через прокси: /device/<ip>/... Так в
// интерфейсе может быть сколько угодно матриц, а контейнеру не нужно знать о них заранее.

// Коды EDID из штатного UI. Копии TV1–TV8 идут как 5–9 и A–C, поэтому
// «копия TV6» хранится как A, а не 10: так устройство отдаёт и принимает их.
// Подписи кодов — в языковых файлах, здесь только значения протокола.
export const EDID_CODES = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "A", "B", "C"];
export const AUDIO_MODES = ["2.0", "5.1", "7.1"];

const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

// Совпадает с правилом в nginx.conf: прокси не ходит за пределы приватных сетей.
export function isPrivateIpv4(ip) {
  const match = IPV4.exec(ip);
  if (!match) return false;
  const octets = match.slice(1).map(Number);
  if (octets.some((octet) => octet > 255)) return false;
  const [a, b] = octets;
  return a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31);
}

// Строка EDID: <код><HDR><3D><30бит><36бит><48бит><аудио>, например 1010001 —
// 1080P, 3D включён, аудио 2.0. Так устройство хранит и отдаёт её, в одной строке.
export function parseEdid(value) {
  const [code, hdr, threeD, bit30, bit36, bit48, audio] = value.padEnd(7, "0");
  return {
    code,
    hdr: hdr === "1",
    threeD: threeD === "1",
    bit30: bit30 === "1",
    bit36: bit36 === "1",
    bit48: bit48 === "1",
    audio: audio === "4" ? "7.1" : audio === "2" ? "5.1" : "2.0",
  };
}

// Ответ AutoGetAllData: 0E=<MAC>&<IP>&<маска>&<шлюз>&<имя>&<DHCP>&<EDID1..8>&<вход1..8>.
// Номера входов в ответе с нуля (поле 0 = вход 1), здесь сразу переводим в единицы.
export function parseState(raw) {
  const fields = raw.split("&");
  fields[0] = fields[0].replace(/^0E=/, "");
  if (fields.length < 22) throw new Error("ответ не похож на матрицу");
  return {
    mac: fields[0],
    ip: fields[1],
    mask: fields[2],
    gateway: fields[3],
    name: fields[4],
    dhcp: fields[5] === "1",
    edid: fields.slice(6, 14).map(parseEdid),
    outputs: fields.slice(14, 22).map((value) => Number(value) + 1),
  };
}

// Случайный суффикс не даёт браузеру и прокси отдать старый ответ вместо нового.
// random передаётся снаружи, чтобы тесты получали предсказуемые пути.
export function deviceBase(ip) {
  return `/device/${ip}`;
}

export function stateUrl(ip, random = Math.random()) {
  return `${deviceBase(ip)}/AutoGetAllData=${random}`;
}

export function revisionUrl(ip, random = Math.random()) {
  return `${deviceBase(ip)}/AutoGetRevID=${random}`;
}

export function routeUrl(ip, output, source, random = Math.random()) {
  return `${deviceBase(ip)}/@PORT${output}=${source}.0.${random}`;
}

export function edidUrl(ip, input, edid, random = Math.random()) {
  const flag = (value) => (value ? "1" : "0");
  const audio = edid.audio === "7.1" ? "4" : edid.audio === "5.1" ? "2" : "1";
  const payload =
    edid.code + flag(edid.hdr) + flag(edid.threeD) + flag(edid.bit30) +
    flag(edid.bit36) + flag(edid.bit48) + audio;
  return `${deviceBase(ip)}/@EDID${input}=${payload}.${random}`;
}

export function networkUrl(ip) {
  return `${deviceBase(ip)}/config.cgi`;
}

// Поля формы IP-настроек штатного UI: ip, sub, gw, MID, DHCP.
// DHCP уходит только когда включён: так форма и отправляется в штатном UI.
export function networkBody({ ip, mask, gateway, name, dhcp }) {
  const form = new URLSearchParams({
    ip: ip ?? "",
    sub: mask ?? "",
    gw: gateway ?? "",
    MID: String(name ?? "").slice(0, 16),
  });
  if (dhcp) form.set("DHCP", "on");
  return form;
}

// ── Адреса матриц: "ip" или "ip:port". Порт по умолчанию 80 и в строке не пишется. ──

export const DEFAULT_PORT = 80;

// Разбор адреса, введённого пользователем. Возвращает { host } или { error: "ip" | "port" }.
// host — нормализованная строка, которая уходит в /device/<host>/...
export function parseHost(value) {
  const match = /^(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?$/.exec(value.trim());
  if (!match || !isPrivateIpv4(match[1])) return { error: "ip" };
  const port = match[2] === undefined ? DEFAULT_PORT : Number(match[2]);
  if (port < 1 || port > 65535) return { error: "port" };
  return { host: port === DEFAULT_PORT ? match[1] : `${match[1]}:${port}` };
}

// ── Поиск в сети: CIDR, только приватные сети и не шире /24 ───────────────────────

export const SCAN_MAX_PREFIX = 24;

function ipToInt(ip) {
  return ip.split(".").reduce((acc, octet) => acc * 256 + Number(octet), 0);
}

function intToIp(value) {
  return [24, 16, 8, 0].map((shift) => Math.floor(value / 2 ** shift) % 256).join(".");
}

// Адреса сети для опроса. Сетевой и широковещательный адреса не опрашиваем.
// Возвращает { hosts } или { error: "badRange" | "notPrivate" | "tooLarge" }.
export function cidrHosts(cidr) {
  const match = /^(\d{1,3}(?:\.\d{1,3}){3})\/(\d{1,2})$/.exec(cidr.trim());
  if (!match) return { error: "badRange" };
  const prefix = Number(match[2]);
  if (prefix > 32 || !isPrivateIpv4(match[1])) return { error: "notPrivate" };
  if (prefix < SCAN_MAX_PREFIX) return { error: "tooLarge" };
  const size = 2 ** (32 - prefix);
  const base = Math.floor(ipToInt(match[1]) / size) * size;
  const hosts = [];
  for (let offset = 1; offset < size - 1; offset += 1) hosts.push(intToIp(base + offset));
  return { hosts };
}
