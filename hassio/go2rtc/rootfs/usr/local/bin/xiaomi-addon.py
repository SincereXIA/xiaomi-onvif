"""Validate add-on options and supervise the matching go2rtc and ONVIF processes."""

import base64
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

OPTIONS_PATH = Path("/data/options.json")
DEFAULTS = {
    "config_path": "/config/go2rtc.yaml",
    "api_url": "http://127.0.0.1:1984",
    "onvif_enabled": False,
    "rtsp_base": "",
    "cameras": [],
    "profile_width": 3840,
    "profile_height": 2160,
    "profile_fps": 20,
    "profile_codec": "H264",
}


class ConfigError(ValueError):
    """An invalid option, without including user values in the error."""


def require(condition, message):
    if not condition:
        raise ConfigError(message)


def parse_url(value, field):
    require(
        isinstance(value, str) and not any(ord(c) < 33 for c in value),
        field + " must be a URL without whitespace",
    )
    try:
        parsed = urllib.parse.urlsplit(value)
        parsed.port
    except ValueError:
        raise ConfigError(field + " has an invalid host or port") from None
    require(
        bool(parsed.hostname) and not parsed.query and not parsed.fragment,
        field + " must include a host and have no query or fragment",
    )
    require(parsed.port is None or parsed.port > 0, field + " must use a valid port")
    return parsed


def stream_name(value):
    require(
        isinstance(value, str)
        and 0 < len(value) <= 256
        and not any(c in ",=" or ord(c) < 32 or ord(c) == 127 for c in value),
        "camera stream names must be nonempty and contain no commas, equals signs or control characters",
    )
    return value


def settings(options, config_root=Path("/config")):
    """Return validated options and argument arrays without invoking a shell."""
    require(
        isinstance(options, dict) and not options.keys() - DEFAULTS.keys(),
        "options must be an object containing supported settings only",
    )
    data = DEFAULTS | options
    path = data["config_path"]
    require(
        isinstance(path, str) and path.startswith("/") and "\x00" not in path,
        "config_path must be an absolute file path under /config",
    )
    config = Path(path)
    require(
        config.resolve().is_relative_to(config_root.resolve())
        and config != config_root,
        "config_path must stay under /config",
    )
    api = parse_url(data["api_url"], "api_url")
    require(
        api.scheme == "http"
        and api.hostname in ("127.0.0.1", "localhost", "::1")
        and (api.port or 80) == 1984
        and api.path in ("", "/"),
        "api_url must point to the local go2rtc HTTP API on port 1984",
    )
    require(type(data["onvif_enabled"]) is bool, "onvif_enabled must be a boolean")
    require(isinstance(data["rtsp_base"], str), "rtsp_base must be a string")
    require(
        isinstance(data["cameras"], list) and len(data["cameras"]) <= 32,
        "cameras must be a list of at most 32 entries",
    )
    for key, maximum in (
        ("profile_width", 16384),
        ("profile_height", 16384),
        ("profile_fps", 120),
    ):
        require(
            type(data[key]) is int and 1 <= data[key] <= maximum,
            key + " is outside its supported range",
        )
    require(
        data["profile_codec"] in ("H264", "H265"), "profile_codec must be H264 or H265"
    )
    ports = {1984, 8554, 8555}
    if data["onvif_enabled"]:
        rtsp = parse_url(data["rtsp_base"], "rtsp_base")
        require(
            rtsp.scheme == "rtsp"
            and rtsp.path in ("", "/")
            and rtsp.hostname not in ("localhost", "0.0.0.0", "::", "::1")
            and not rtsp.hostname.startswith("127."),
            "rtsp_base must be a LAN-accessible RTSP base URL",
        )
        ports.add(rtsp.port or 554)
        require(bool(data["cameras"]), "ONVIF requires at least one camera")
    command = [
        "/usr/local/bin/xiaomi-onvif",
        "-go2rtc",
        data["api_url"],
        "-rtsp",
        data["rtsp_base"],
    ]
    for key in ("profile_width", "profile_height", "profile_fps", "profile_codec"):
        command += ["-" + key.replace("_", "-"), str(data[key])]
    names = set()
    for camera in data["cameras"]:
        require(
            isinstance(camera, dict)
            and {"stream", "port"} <= camera.keys()
            and not camera.keys() - {"stream", "rtsp_stream", "port"},
            "each camera requires stream and port, with optional rtsp_stream",
        )
        name = stream_name(camera["stream"])
        target = stream_name(camera.get("rtsp_stream", name))
        port = camera["port"]
        require(
            type(port) is int and 1024 <= port <= 65535,
            "camera ports must be integers between 1024 and 65535",
        )
        require(
            port not in ports, "camera ports must be unique and avoid go2rtc listeners"
        )
        require(name not in names, "camera streams must be unique")
        ports.add(port)
        names.add(name)
        command += ["-camera", f"{name}=:{port},{target}"]
    return {
        "options": data,
        "config": config,
        "go2rtc": ["/usr/local/bin/go2rtc", "-config", str(config)],
        "onvif": command if data["onvif_enabled"] else None,
    }


def load_settings():
    """Read Supervisor options with a bounded input size."""
    try:
        with OPTIONS_PATH.open("rb") as source:
            payload = source.read(65537)
        require(len(payload) <= 65536, "options.json exceeds 64 KiB")
        return settings(json.loads(payload))
    except (OSError, UnicodeError, json.JSONDecodeError):
        raise ConfigError("cannot read valid JSON from /data/options.json") from None


def prepare_config(config):
    """Create an empty configuration only when the selected file is absent."""
    config.parent.mkdir(parents=True, exist_ok=True)
    try:
        with open(
            config, "x", opener=lambda path, flags: os.open(path, flags, 0o600)
        ) as target:
            target.write("streams: {}\n")
    except FileExistsError:
        require(config.is_file(), "config_path must point to a regular file")


def http_ok(url):
    """Probe locally without proxy settings or logging credentials."""
    parsed = urllib.parse.urlsplit(url)
    host = parsed.hostname
    if ":" in host:
        host = "[" + host + "]"
    clean = urllib.parse.urlunsplit(
        (parsed.scheme, f"{host}:{parsed.port or 80}", parsed.path, "", "")
    )
    headers = {}
    if parsed.username is not None:
        credentials = (
            urllib.parse.unquote(parsed.username)
            + ":"
            + urllib.parse.unquote(parsed.password or "")
        )
        headers["Authorization"] = (
            "Basic " + base64.b64encode(credentials.encode()).decode()
        )
    try:
        opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect()
        )
        with opener.open(
            urllib.request.Request(clean, headers=headers), timeout=1
        ) as response:
            return response.status == 200
    except urllib.error.HTTPError as error:
        error.close()
        return False
    except (OSError, ValueError, urllib.error.URLError):
        return False


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never forward a local health probe or its credentials elsewhere."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def health_urls(config):
    urls = [config["options"]["api_url"].rstrip("/") + "/api"]
    if config["onvif"]:
        urls += [
            f"http://127.0.0.1:{camera['port']}/healthz"
            for camera in config["options"]["cameras"]
        ]
    return urls


def wait_ready(urls, children, stopped, timeout=30):
    deadline = time.monotonic() + timeout
    while not stopped.is_set() and time.monotonic() < deadline:
        if any(child.poll() is not None for child in children):
            return False
        if all(http_ok(url) for url in urls):
            return True
        stopped.wait(0.2)
    return False


def supervise(config):
    """Stop both programs on shutdown or when either required program exits."""
    stopped = threading.Event()
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, lambda *_: stopped.set())
    children = []
    try:
        prepare_config(config["config"])
        children.append(subprocess.Popen(config["go2rtc"]))
        urls = health_urls(config)
        if not wait_ready(urls[:1], children, stopped):
            if not stopped.is_set():
                print(
                    "Local go2rtc API did not become ready on port 1984",
                    file=sys.stderr,
                )
            return 0 if stopped.is_set() else 1
        if config["onvif"] and not stopped.is_set():
            children.append(subprocess.Popen(config["onvif"]))
            if not wait_ready(urls, children, stopped):
                if not stopped.is_set():
                    print("ONVIF bridge did not become ready", file=sys.stderr)
                return 0 if stopped.is_set() else 1
        while not stopped.wait(0.2):
            if any(child.poll() is not None for child in children):
                print("A required service exited; stopping the add-on", file=sys.stderr)
                return 1
        return 0
    finally:
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()


def main():
    try:
        config = load_settings()
        if sys.argv[1:] == ["health"]:
            return 0 if all(http_ok(url) for url in health_urls(config)) else 1
        if sys.argv[1:] == ["run"]:
            return supervise(config)
        raise ConfigError("expected run or health")
    except ConfigError as error:
        print("Add-on configuration error: " + str(error), file=sys.stderr)
        return 1
    except OSError:
        print(
            "Cannot access the configuration or start a required service",
            file=sys.stderr,
        )
        return 1


if __name__ == "__main__":
    sys.exit(main())
