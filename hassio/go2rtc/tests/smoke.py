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
                    "profile_width": 1920,
                    "profile_height": 1080,
                    "profile_fps": 30,
                    "cameras": [
                        {
                            "stream": "c700",
                            "rtsp_stream": "c700_4k",
                            "port": 8891,
                            "profile_width": 3840,
                            "profile_height": 2160,
                            "profile_fps": 20,
                            "profile_codec": "H265",
                        },
                        {"stream": "door", "port": 8892, "profile_fps": 15},
                        {"stream": "garage", "port": 8893},
                    ],
                },
            )
            wait_for(lambda: healthy(name), "go2rtc and ONVIF readiness")
            probe = """import urllib.request
import xml.etree.ElementTree as ET
ns = {'tt': 'http://www.onvif.org/ver10/schema'}
def soap(port, action):
    body = f'<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><{action} xmlns="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>'.encode()
    req = urllib.request.Request(f'http://127.0.0.1:{port}/onvif/media_service', data=body, headers={'Content-Type':'application/soap+xml'})
    with urllib.request.urlopen(req, timeout=3) as response:
        return ET.fromstring(response.read())
# Repeat in a different order to detect profile leakage between listeners.
for port, stream, width, height, fps, codec in [
    (8891, 'c700_4k', 3840, 2160, 20, 'H265'),
    (8892, 'door', 1920, 1080, 15, 'H264'),
    (8893, 'garage', 1920, 1080, 30, 'H264'),
    (8891, 'c700_4k', 3840, 2160, 20, 'H265'),
]:
    uri = soap(port, 'GetStreamUri')
    assert uri.find('.//tt:Uri', ns).text == f'rtsp://192.0.2.10:8554/{stream}'
    for action in ['GetProfiles', 'GetProfile', 'GetVideoSources']:
        root = soap(port, action)
        assert root.find('.//tt:Width', ns).text == str(width)
        assert root.find('.//tt:Height', ns).text == str(height)
        if action == 'GetVideoSources':
            assert root.find('.//tt:Framerate', ns).text == str(fps)
        else:
            assert root.find('.//tt:FrameRateLimit', ns).text == str(fps)
            assert root.find('.//tt:Encoding', ns).text == codec
            bounds = root.find('.//tt:Bounds', ns)
            assert bounds.get('width') == str(width) and bounds.get('height') == str(height)
"""
            docker("exec", name, "python3", "-c", probe)
            assert source.read_text() == original, (
                "Existing configuration was overwritten"
            )
            docker("stop", "--time", "15", name)
            assert inspect(name, ".State.ExitCode") == "0", "Shutdown was not graceful"
            print(
                "PASS: mixed camera profiles, partial/default inheritance, SOAP methods and RTSP URIs, config preservation, graceful stop"
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
            # Container root owns this private file; the CI runner cannot read it.
            docker(
                "exec",
                name,
                "python3",
                "-c",
                "from pathlib import Path; p=Path('/config/go2rtc.yaml'); "
                "assert p.read_text() == 'streams: {}\\n'; "
                "assert p.stat().st_mode & 0o777 == 0o600",
            )
            docker("stop", "--time", "15", name)
            assert inspect(name, ".State.ExitCode") == "0"
            print(
                "PASS: missing configuration is created privately and starts successfully"
            )

            source.unlink()
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
