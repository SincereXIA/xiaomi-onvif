"""Exercise the real image in isolated Docker networks without camera credentials."""

import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def docker(*args, check=True):
    return subprocess.run(
        ["docker", *args], check=check, text=True, capture_output=True
    )


def inspect(name, field):
    return docker("inspect", "--format", "{{" + field + "}}", name).stdout.strip()


def wait_for(predicate, description, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.25)
    raise AssertionError("Timed out waiting for " + description)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("image")
    args = parser.parse_args()
    prefix = "xiaomi-addon-smoke-" + uuid.uuid4().hex[:12]
    names = []
    with tempfile.TemporaryDirectory(prefix=prefix) as folder:
        root = Path(folder)
        config = root / "config"
        config.mkdir()
        source = config / "go2rtc.yaml"
        original = "# Preserve this file byte-for-byte\napi:\n  listen: ':1984'\nrtsp:\n  listen: ':8554'\nwebrtc:\n  listen: ''\nstreams: {}\n"
        source.write_text(original)

        def start(suffix, options):
            name = prefix + "-" + suffix
            data = root / suffix
            data.mkdir()
            (data / "options.json").write_text(json.dumps(options))
            names.append(name)
            docker(
                "run",
                "-d",
                "--name",
                name,
                "--network",
                "bridge",
                "--mount",
                f"type=bind,src={config},dst=/config",
                "--mount",
                f"type=bind,src={data},dst=/data",
                args.image,
            )
            return name

        def healthy(name):
            if inspect(name, ".State.Running") != "true":
                raise AssertionError("Container exited before becoming ready: " + name)
            return (
                docker(
                    "exec",
                    name,
                    "python3",
                    "/usr/local/bin/xiaomi-addon.py",
                    "health",
                    check=False,
                ).returncode
                == 0
            )

        try:
            name = start(
                "enabled",
                {
                    "onvif_enabled": True,
                    "rtsp_base": "rtsp://192.0.2.10:8554",
                    "cameras": [
                        {"stream": "c700", "rtsp_stream": "c700_h264", "port": 8891}
                    ],
                },
            )
            wait_for(lambda: healthy(name), "go2rtc and ONVIF readiness")
            probe = """import urllib.request
body = b'<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>'
req = urllib.request.Request('http://127.0.0.1:8891/onvif/media_service', data=body, headers={'Content-Type':'application/soap+xml'})
with urllib.request.urlopen(req, timeout=3) as response:
    assert b'rtsp://192.0.2.10:8554/c700_h264' in response.read()
"""
            docker("exec", name, "python3", "-c", probe)
            assert source.read_text() == original, (
                "Existing configuration was overwritten"
            )
            docker("stop", "--time", "15", name)
            assert inspect(name, ".State.ExitCode") == "0", "Shutdown was not graceful"
            print(
                "PASS: enabled bridge, API/health, SOAP stream URI, config preservation, graceful stop"
            )

            name = start("disabled", {})
            wait_for(lambda: healthy(name), "go2rtc-only readiness")
            docker(
                "exec",
                name,
                "python3",
                "-c",
                "import socket; s=socket.socket(); s.settimeout(1); assert s.connect_ex(('127.0.0.1',8891)) != 0",
            )
            # A service failure must stop the container so Supervisor can restart it.
            docker("exec", name, "sh", "-c", "kill -KILL $(pidof go2rtc)")
            wait_for(
                lambda: inspect(name, ".State.Running") == "false",
                "container exit after go2rtc failure",
            )
            assert inspect(name, ".State.ExitCode") != "0"
            print(
                "PASS: disabled bridge has no listener; service crash stops the container"
            )

            name = start(
                "bridge-crash",
                {
                    "onvif_enabled": True,
                    "rtsp_base": "rtsp://192.0.2.10:8554",
                    "cameras": [{"stream": "c700", "port": 8891}],
                },
            )
            wait_for(lambda: healthy(name), "ONVIF readiness before failure")
            docker("exec", name, "sh", "-c", "kill -KILL $(pidof xiaomi-onvif)")
            wait_for(
                lambda: inspect(name, ".State.Running") == "false",
                "container exit after bridge failure",
            )
            assert inspect(name, ".State.ExitCode") != "0"
            print("PASS: bridge crash stops the container")

            name = start(
                "invalid", {"api_url": "http://secret-user:secret-password@remote:1984"}
            )
            wait_for(
                lambda: inspect(name, ".State.Running") == "false",
                "invalid-option rejection",
            )
            assert inspect(name, ".State.ExitCode") != "0"
            logs = docker("logs", name).stdout + docker("logs", name).stderr
            assert "secret-password" not in logs and "secret-user" not in logs
            assert "Add-on configuration error" in logs
            print("PASS: invalid options fail closed without logging credentials")

            source.unlink()
            name = start("generated", {})
            wait_for(lambda: healthy(name), "startup with a missing config file")
            assert source.read_text() == "streams: {}\n"
            assert source.stat().st_mode & 0o777 == 0o600
            docker("stop", "--time", "15", name)
            assert inspect(name, ".State.ExitCode") == "0"
            print(
                "PASS: missing configuration is created privately and starts successfully"
            )

            source.write_text(
                original.replace(
                    "  listen: ':1984'",
                    "  listen: ':1984'\n  username: test\n  password: test\n  local_auth: true",
                )
            )
            name = start("auth", {"api_url": "http://test:test@127.0.0.1:1984"})
            wait_for(lambda: healthy(name), "authenticated local API readiness")
            docker("stop", "--time", "15", name)
            assert inspect(name, ".State.ExitCode") == "0"
            print("PASS: readiness and health probes support authenticated local APIs")
        finally:
            for name in names:
                docker("rm", "-f", name, check=False)


if __name__ == "__main__":
    main()
