// The matrix protocol as the device's own web UI uses it.
// A module without DOM access: it can be tested in Node, apart from the page.
//
// Each matrix is addressed by its IP through the proxy: /device/<ip>/... This way the
// interface can have any number of matrices, and the container does not need to know them in advance.

// EDID codes from the device's own UI. Copies of TV1–TV8 use codes 5–9 and A–C, so
// "copy of TV6" is stored as A, not 10: that is how the device sends and accepts them.
// Code labels live in the language files; this file holds only the protocol values.
export const EDID_CODES = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "A", "B", "C"];
export const AUDIO_MODES = ["2.0", "5.1", "7.1"];

const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

// Matches the rule in proxy.go: the proxy does not go beyond private networks.
export function isPrivateIpv4(ip) {
  const match = IPV4.exec(ip);
  if (!match) return false;
  const octets = match.slice(1).map(Number);
  if (octets.some((octet) => octet > 255)) return false;
  const [a, b] = octets;
  return a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31);
}

// EDID string: <code><HDR><3D><30-bit><36-bit><48-bit><audio>, for example 1010001 —
// 1080P, 3D on, audio 2.0. The device stores and returns it as one string.
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

// AutoGetAllData response: 0E=<MAC>&<IP>&<mask>&<gateway>&<name>&<DHCP>&<EDID1..8>&<input1..8>.
// Input numbers in the response start at zero (field 0 = input 1); they are converted to one-based here.
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

// A random suffix stops the browser and the proxy from returning a stale response instead of a new one.
// random is passed in from outside so that tests get predictable paths.
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

// Fields of the IP settings form in the device's own UI: ip, sub, gw, MID, DHCP.
// DHCP is sent only when enabled: this is how the device's own UI submits the form.
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

// ── Matrix addresses: "ip" or "ip:port". The default port is 80 and is not written in the string. ──

export const DEFAULT_PORT = 80;

// Parses an address typed by the user. Returns { host } or { error: "ip" | "port" }.
// host is the normalized string used in /device/<host>/...
export function parseHost(value) {
  const match = /^(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?$/.exec(value.trim());
  if (!match || !isPrivateIpv4(match[1])) return { error: "ip" };
  const port = match[2] === undefined ? DEFAULT_PORT : Number(match[2]);
  if (port < 1 || port > 65535) return { error: "port" };
  return { host: port === DEFAULT_PORT ? match[1] : `${match[1]}:${port}` };
}

// ── Network discovery: CIDR, private networks only, no wider than /24 ───────────────────────

export const SCAN_MAX_PREFIX = 24;

function ipToInt(ip) {
  return ip.split(".").reduce((acc, octet) => acc * 256 + Number(octet), 0);
}

function intToIp(value) {
  return [24, 16, 8, 0].map((shift) => Math.floor(value / 2 ** shift) % 256).join(".");
}

// Addresses of the network to probe. The network and broadcast addresses are skipped.
// Returns { hosts } or { error: "badRange" | "notPrivate" | "tooLarge" }.
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
