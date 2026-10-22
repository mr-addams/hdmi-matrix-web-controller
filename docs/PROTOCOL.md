# Matrix protocol notes

*hdmi-matrix-web-controller is a standalone micro-service for HDMI matrix switchers, for local networks only.*

Notes collected while building the controller against a ZENTY ZT-MT88A (device name `8*8HDMI2.0Matrix`). Everything here was observed on the device or read from the device's own web UI code, unless marked otherwise.

## HTTP (port 80) — used by the controller

All requests are plain `GET`s without authentication. A random suffix (`=0.123…`) prevents caching.

| Request | Response / effect |
| --- | --- |
| `/AutoGetAllData=<r>` | `0E=<MAC>&<IP>&<mask>&<gateway>&<name>&<DHCP 0/1>&<EDID 1..8>&<input for output 1..8>` |
| `/AutoGetRevID=<r>` | `52=<revision>` — used to recognise a compatible device |
| `/@PORT<out>=<in>.0.<r>` | Switch output `out` (1–8) to input `in` (1–8). Responds with an empty body or drops the connection; the switch still happens. |
| `/@EDID<in>=<payload>.<r>` | Set EDID for input `in`. Payload: `<code><HDR><3D><30b><36b><48b><audio>`, flags `0/1`, audio `1` = 2.0, `2` = 5.1, `4` = 7.1 |
| `POST /config.cgi` | Network form: `ip`, `sub`, `gw`, `MID` (device name, ≤16 chars), `DHCP=on` when enabled |

Codes: `1` 1080P, `2` 4K 30 Hz, `3` 4K 60 Hz 4:2:0, `4` 4K 60 Hz 4:4:4, `5`–`9` and `A`–`C` copy EDID from display 1–8 (`A` = display 6, not 10).

In `AutoGetAllData`, input numbers are **zero-based** (`0` = input 1), while the `@PORT` request takes one-based numbers.

Writes are not acknowledged: a successful switch is confirmed only by reading the state back.

## HTTP/Web UI

The device's own page is served from port 80 and has no login. It has three tabs (Port Set, EDID Config, IP Config). A separate administrator login is not required for the web UI.

## Telnet (port 23)

A command-line interface answers on port 23 and asks for a password (`Password:`). Its password is not documented, and the controller does not use telnet.

## TCP 5000 — not used

The vendor's Savant driver describes a TCP control protocol on port 5000 with commands of the form `@T <OO> <II> #` (CRLF-terminated), where `OO` selects an output and `II` an input. In tests against the device this port accepted connections but did not respond to these commands, so the controller does not depend on it.
