"""Configuration and packaging regression tests for the Home Assistant add-on."""

import importlib.util
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

import yaml

ADDON = Path(__file__).resolve().parents[1]
ROOT = ADDON.parents[1]
spec = importlib.util.spec_from_file_location(
    "runtime", ADDON / "rootfs/usr/local/bin/xiaomi-addon.py"
)
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.options = {"config_path": str(self.root / "go2rtc.yaml")}

    def settings(self, **options):
        return runtime.settings(self.options | options, self.root)

    def enabled(self, **options):
        return self.settings(
            **(
                {
                    "onvif_enabled": True,
                    "rtsp_base": "rtsp://192.0.2.10:8654",
                    "cameras": [{"stream": "c700", "port": 8891}],
                }
                | options
            )
        )

    def test_manifest_defaults_match_runtime(self):
        config = yaml.safe_load((ADDON / "config.yaml").read_text())
        self.assertEqual(config["options"], runtime.DEFAULTS)
        self.assertFalse(config["init"])
        self.assertTrue(config["host_network"])
        self.assertEqual(config["ingress_port"], 1984)
        self.assertEqual(set(config["arch"]), {"amd64", "aarch64"})
        self.assertNotIn("privileged", config)
        self.assertNotIn("docker_api", config)
        mapping = next(m for m in config["map"] if m["type"] == "homeassistant_config")
        self.assertEqual(mapping["path"], "/config")
        self.assertFalse(mapping["read_only"])

    def test_disabled_bridge_has_no_command(self):
        self.assertIsNone(self.settings()["onvif"])
        self.assertEqual(len(runtime.health_urls(self.settings())), 1)

    def test_missing_config_created_private_and_existing_bytes_preserved(self):
        config = self.settings()["config"]
        runtime.prepare_config(config)
        self.assertEqual(config.read_text(), "streams: {}\n")
        self.assertEqual(config.stat().st_mode & 0o777, 0o600)
        original = b"# credentials and comments must survive\nstreams: {}\n"
        config.write_bytes(original)
        runtime.prepare_config(config)
        self.assertEqual(config.read_bytes(), original)

    def test_nested_config_is_supported(self):
        config = self.settings(config_path=str(self.root / "nested/camera.yaml"))[
            "config"
        ]
        runtime.prepare_config(config)
        self.assertTrue(config.is_file())

    def test_paths_cannot_escape_config_mapping(self):
        with tempfile.TemporaryDirectory() as outside:
            (self.root / "link").symlink_to(outside, target_is_directory=True)
            for path in [
                outside + "/secret",
                str(self.root / "../secret"),
                str(self.root / "link/secret"),
                "relative.yaml",
                str(self.root),
            ]:
                with self.subTest(path=path), self.assertRaises(runtime.ConfigError):
                    self.settings(config_path=path)

    def test_camera_mapping_keeps_control_and_rtsp_stream_names(self):
        config = self.enabled(
            cameras=[{"stream": "c700", "rtsp_stream": "c700_h264", "port": 8891}]
        )
        command = config["onvif"]
        self.assertEqual(command[-2:], ["-camera", "c700=:8891,c700_h264"])
        self.assertIn("rtsp://192.0.2.10:8654", command)
        self.assertEqual(
            runtime.health_urls(config)[1], "http://127.0.0.1:8891/healthz"
        )

    def test_arguments_are_literal_without_shell_interpolation(self):
        name = "camera $(touch /tmp/never-execute);`id`"
        config = self.enabled(cameras=[{"stream": name, "port": 8891}])
        self.assertEqual(config["onvif"][-1], f"{name}=:8891,{name}")

    def test_ambiguous_stream_names_rejected(self):
        for name in ["", "a=b", "a,b", "a\n", "a\x00", "a\x7f", 42]:
            with self.subTest(name=name), self.assertRaises(runtime.ConfigError):
                self.enabled(cameras=[{"stream": name, "port": 8891}])

    def test_invalid_or_duplicate_ports_rejected(self):
        for port in [0, 80, 1984, 8554, 8555, 8654, 65536, "8891", True]:
            with self.subTest(port=port), self.assertRaises(runtime.ConfigError):
                self.enabled(cameras=[{"stream": "c700", "port": port}])
        with self.assertRaises(runtime.ConfigError):
            self.enabled(
                cameras=[{"stream": "a", "port": 8891}, {"stream": "b", "port": 8891}]
            )
        with self.assertRaises(runtime.ConfigError):
            self.enabled(
                cameras=[{"stream": "a", "port": 8891}, {"stream": "a", "port": 8892}]
            )

    def test_onvif_needs_cameras_and_lan_rtsp_url(self):
        with self.assertRaises(runtime.ConfigError):
            self.enabled(cameras=[])
        for url in [
            "",
            "https://example.org",
            "rtsp://localhost:8554",
            "rtsp://127.0.0.2:8554",
            "rtsp://[::1]:8554",
            "rtsp://0.0.0.0:8554",
            "rtsp://ha:8554/stream",
            "rtsp://ha:8554?password=secret",
            "rtsp://ha:bad",
            "rtsp://ha:0",
        ]:
            with self.subTest(url=url), self.assertRaises(runtime.ConfigError):
                self.enabled(rtsp_base=url)

    def test_api_stays_local_for_managed_go2rtc_and_ingress(self):
        for url in [
            "http://example.org:1984",
            "http://localhost:8554",
            "https://localhost:1984",
            "http://localhost:1984/path",
            "http://localhost:1984?token=secret",
        ]:
            with self.subTest(url=url), self.assertRaises(runtime.ConfigError):
                self.settings(api_url=url)
        self.settings(api_url="http://user:pass@127.0.0.1:1984")

    def test_invalid_profile_and_option_types_rejected(self):
        for options in [
            {"profile_width": 0},
            {"profile_height": True},
            {"profile_fps": 121},
            {"profile_codec": "AV1"},
            {"onvif_enabled": "true"},
            {"cameras": {}},
            {"cameras": [None]},
            {"unknown": "value"},
        ]:
            with self.subTest(options=options), self.assertRaises(runtime.ConfigError):
                self.settings(**options)

    def test_bad_options_do_not_leak_secrets(self):
        path = self.root / "options.json"
        for payload in [b"{secret-password", b"x" * 65537, b"[]"]:
            path.write_bytes(payload)
            with (
                patch.object(runtime, "OPTIONS_PATH", path),
                self.assertRaises(runtime.ConfigError) as caught,
            ):
                runtime.load_settings()
            self.assertNotIn("secret-password", str(caught.exception))
        with self.assertRaises(runtime.ConfigError) as caught:
            self.settings(api_url="http://secret-user:secret-password@remote:1984")
        self.assertNotIn("secret-password", str(caught.exception))

    def test_read_supervisor_options(self):
        path = self.root / "options.json"
        path.write_text(json.dumps({}))
        with patch.object(runtime, "OPTIONS_PATH", path):
            self.assertIsNone(runtime.load_settings()["onvif"])

    def test_health_probes_support_basic_auth_and_do_not_follow_redirects(self):
        calls = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                calls.append(self.path)
                if self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/must-not-be-requested")
                elif (
                    self.headers.get("Authorization")
                    == "Basic " + base64.b64encode(b"user:p@ss").decode()
                ):
                    self.send_response(200)
                else:
                    self.send_response(401)
                self.end_headers()

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.addCleanup(server.server_close)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        base = f"http://user:p%40ss@127.0.0.1:{server.server_port}"
        self.assertTrue(runtime.http_ok(base + "/api"))
        self.assertFalse(runtime.http_ok(base + "/redirect"))
        self.assertEqual(calls, ["/api", "/redirect"])


if __name__ == "__main__":
    unittest.main()
