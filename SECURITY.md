# Security policy

## Supported versions

Only the latest published release receives security fixes while the project is
in beta.

## Reporting a vulnerability

Do not open a public issue for a vulnerability or include Xiaomi credentials,
camera URLs, account IDs, device IDs, tokens, private IP inventories or raw
configuration files in a report.

Use GitHub private vulnerability reporting for this repository. If that feature
is unavailable, contact the repository owner privately through the address in
their GitHub profile.

Include the affected version, deployment topology, impact and minimal sanitized
reproduction. You should receive an acknowledgement within seven days.

## Deployment warning

The ONVIF and go2rtc control APIs are designed for trusted local networks and
may move physical camera hardware. Keep them behind a firewall or isolated
container network. They must not be exposed directly to the public Internet.
