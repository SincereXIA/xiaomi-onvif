# go2rtc with Xiaomi ONVIF

Experimental Home Assistant Add-on for **amd64** and **aarch64**. It runs this
repository's matching go2rtc and optional Xiaomi ONVIF bridge in one container.
The regular Docker/Compose image remains available separately.

See [DOCS.md](DOCS.md) for migration, options, network access and rollback.
Container tests do not establish HAOS compatibility. Supervisor installation,
Ingress, backup/restore and upgrades still need real HAOS validation.

The first Add-on image must be published before installation from the store.
The repository metadata alone does not make an unpublished image installable.
