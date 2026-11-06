# Security policy

## Supported versions

Only the latest commit on `main` receives security fixes.

## Reporting a vulnerability

Please **do not** open a public issue for security problems. Use GitHub's private vulnerability reporting for this repository (Security tab → Report a vulnerability). Include the affected file, steps to reproduce and the impact.

## Known limitations

- The HDMI matrix's own web API has no authentication. Anyone who can reach the device on the network can switch inputs and change its settings. This is a property of the device, not something the controller can fix. Keep matrices on a trusted network segment.
- The controller's proxy only forwards to private addresses and ports 1–65535. Changes that widen this scope need a clear justification.
- The default sign-in password (`admin`) is public. Change `UI_PASSWORD` before exposing the page.
