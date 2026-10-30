"""Exercise HA's ONVIF initialization calls against the actual bridge binary.

Uses the client pinned by Home Assistant Core 2026.10.4. This runs the SOAP
handshake and HA's profile/PTZ checks, not Home Assistant Core or real cameras.
"""

import asyncio
from datetime import datetime, timezone
import os
import logging
from pathlib import Path
import socket
import unittest
from types import SimpleNamespace

from aiohttp import ClientSession, web
import onvif
from onvif import ONVIFCamera
from onvif.exceptions import ONVIFError
from zeep.exceptions import Fault

DEVICE_NS = "http://www.onvif.org/ver10/device/wsdl"
MEDIA_NS = "http://www.onvif.org/ver10/media/wsdl"
PTZ_NS = "http://www.onvif.org/ver20/ptz/wsdl"
logging.getLogger("asyncio").setLevel(logging.WARNING)


def free_ports(count):
    """Reserve distinct loopback ports until the process is ready to start."""
    sockets = []
    try:
        for _ in range(count):
            sock = socket.socket()
            sock.bind(("127.0.0.1", 0))
            sockets.append(sock)
        return [sock.getsockname()[1] for sock in sockets]
    finally:
        for sock in sockets:
            sock.close()


class HAClientTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.calls = []
        self.preset_list = [{"token": "1", "name": "Desk & window"}]
        self.preset_error = False
        self.move_delay = 0
        self.move_finished = asyncio.Event()
        self.move_connection_closed = False
        self.clients = []
        self.process = None
        self.runner = web.AppRunner(self.mock_backend(), access_log=None)
        await self.runner.setup()
        self.addAsyncCleanup(self.runner.cleanup)
        site = web.TCPSite(self.runner, "127.0.0.1", 0)
        await site.start()
        self.backend_port = site._server.sockets[0].getsockname()[1]
        self.ports = free_ports(3)
        self.addAsyncCleanup(self.stop_bridge)
        await self.start_bridge()

    def mock_backend(self):
        async def presets(request):
            self.calls.append((request.method, request.path, dict(request.query)))
            if request.method == "GET":
                if self.preset_error:
                    return web.Response(status=502, text="Preset discovery failed")
                return web.json_response(self.preset_list)
            if request.query.get("token") not in [p["token"] for p in self.preset_list]:
                return web.Response(status=400, text="Preset token was not discovered")
            return web.json_response({"ok": True})

        async def movement(request):
            self.calls.append((request.method, request.path, dict(request.query)))
            if self.move_delay:
                await asyncio.sleep(self.move_delay)
                self.move_connection_closed = (
                    request.transport is None or request.transport.is_closing()
                )
                self.move_finished.set()
            return web.json_response({"ok": True})

        app = web.Application()
        app.router.add_route("*", "/api/xiaomi/presets", presets)
        app.router.add_post("/api/xiaomi/ptz", movement)
        return app

    async def start_bridge(self):
        binary = os.environ.get("ONVIF_BRIDGE_BINARY", "/tmp/xiaomi-onvif-ha-test")
        self.assertTrue(
            Path(binary).is_file(), f"Build ONVIF_BRIDGE_BINARY first: {binary}"
        )
        self.process = await asyncio.create_subprocess_exec(
            binary,
            "-go2rtc",
            f"http://127.0.0.1:{self.backend_port}",
            "-rtsp",
            "rtsp://192.0.2.10:8554",
            "-profile-width=1920",
            "-profile-height=1080",
            "-profile-fps=25",
            f"-camera=living=127.0.0.1:{self.ports[0]},living_h264",
            f"-camera=door=127.0.0.1:{self.ports[1]},door_h264",
            f"-camera=native=127.0.0.1:{self.ports[2]},native_h265",
            "-camera-profile=native=width=3840,height=2160,fps=20,codec=H265",
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )
        async with asyncio.timeout(10), ClientSession() as session:
            while True:
                self.assertIsNone(
                    self.process.returncode, "Bridge exited before readiness"
                )
                try:
                    async with session.get(
                        f"http://127.0.0.1:{self.ports[0]}/healthz"
                    ) as response:
                        if response.status == 200:
                            break
                except OSError:
                    pass
                await asyncio.sleep(0.02)

    async def stop_bridge(self):
        for client in self.clients:
            await client.close()
        self.clients.clear()
        if self.process is not None and self.process.returncode is None:
            self.process.terminate()
            try:
                await asyncio.wait_for(self.process.wait(), 10)
            except TimeoutError:
                self.process.kill()
                await self.process.wait()
                raise

    async def client(self, index=0, credentials=("", "")):
        camera = ONVIFCamera(
            "127.0.0.1",
            self.ports[index],
            *credentials,
            str(Path(onvif.__file__).parent / "wsdl"),
            no_cache=True,
        )
        self.clients.append(camera)
        await camera.update_xaddrs()
        return camera

    async def test_ha_configuration_and_device_initialization(self):
        # Match config_flow.async_setup_profiles: discovery, unique ID, H264 gate.
        camera = await self.client()
        self.assertEqual(set(camera.xaddrs), {DEVICE_NS, MEDIA_NS, PTZ_NS})
        device = await camera.create_devicemgmt_service()
        interfaces = await device.GetNetworkInterfaces()
        self.assertEqual(len(interfaces), 1)
        self.assertTrue(interfaces[0].Enabled)
        identity = interfaces[0].Info.HwAddress
        self.assertRegex(identity, r"^[0-9a-f]{2}(:[0-9a-f]{2}){5}$")
        self.assertEqual(int(identity[:2], 16) & 3, 2)
        info = await device.GetDeviceInformation()
        self.assertEqual(info.SerialNumber, "living")
        media = await camera.create_media_service()
        profiles = await media.GetProfiles()
        usable = [
            p
            for p in profiles
            if p.VideoEncoderConfiguration
            and p.VideoEncoderConfiguration.Encoding == "H264"
        ]
        self.assertEqual(len(usable), 1)

        # Match ONVIFDevice.async_setup: capabilities, time, info, profiles, PTZ.
        capabilities = await camera.get_capabilities()
        self.assertTrue(capabilities["Media"]["XAddr"].endswith("/onvif/media_service"))
        self.assertTrue(capabilities["PTZ"]["XAddr"].endswith("/onvif/ptz_service"))
        self.assertFalse(capabilities.get("Events"))
        clock = await device.GetSystemDateAndTime()
        value = clock.UTCDateTime
        reported = datetime(
            value.Date.Year,
            value.Date.Month,
            value.Date.Day,
            value.Time.Hour,
            value.Time.Minute,
            value.Time.Second,
            tzinfo=timezone.utc,
        )
        self.assertLess(abs((datetime.now(timezone.utc) - reported).total_seconds()), 5)
        self.assertEqual(
            (await device.GetNetworkInterfaces())[0].Info.HwAddress, identity
        )
        device_caps = await device.GetServiceCapabilities()
        self.assertFalse(device_caps.Security.HttpDigest)
        self.assertFalse(device_caps.Security.UsernameToken)
        media_caps = await media.GetServiceCapabilities()
        self.assertIs(media_caps.SnapshotUri, False)
        self.assertEqual(media_caps.ProfileCapabilities.MaximumNumberOfProfiles, 1)
        self.assertTrue(media_caps.StreamingCapabilities.RTP_RTSP_TCP)
        self.assertFalse(
            media_caps._attr_1, "Media capabilities must not contain PTZ attributes"
        )
        profile = (await media.GetProfiles())[0]
        self.assertEqual(profile.VideoEncoderConfiguration.Resolution.Width, 1920)
        self.assertEqual(
            profile.VideoEncoderConfiguration.RateControl.FrameRateLimit, 25
        )
        self.assertIsNotNone(
            profile.PTZConfiguration.DefaultContinuousPanTiltVelocitySpace
        )
        self.assertIsNotNone(
            profile.PTZConfiguration.DefaultRelativePanTiltTranslationSpace
        )
        self.assertIsNone(profile.PTZConfiguration.DefaultAbsolutePantTiltPositionSpace)
        ptz = await camera.create_ptz_service()
        self.assertFalse(
            (await ptz.GetServiceCapabilities()).GetCompatibleConfigurations
        )
        presets = await ptz.GetPresets(profile.token)
        self.assertEqual([(p.token, p.Name) for p in presets], [("1", "Desk & window")])
        configs = await ptz.GetConfigurations()
        config = await ptz.GetConfiguration({"PTZConfigurationToken": configs[0].token})
        options = await ptz.GetConfigurationOptions(
            {"ConfigurationToken": config.token}
        )
        self.assertTrue(options.Spaces.ContinuousPanTiltVelocitySpace)
        self.assertTrue(options.Spaces.RelativePanTiltTranslationSpace)
        nodes = await ptz.GetNodes()
        self.assertEqual(
            (await ptz.GetNode({"NodeToken": nodes[0].token})).Name, "living PTZ"
        )
        self.assertEqual(
            (await ptz.GetStatus({"ProfileToken": profile.token})).MoveStatus.PanTilt,
            "IDLE",
        )
        uri = await media.GetStreamUri(
            {
                "ProfileToken": profile.token,
                "StreamSetup": {
                    "Stream": "RTP-Unicast",
                    "Transport": {"Protocol": "RTSP"},
                },
            }
        )
        self.assertEqual(uri.Uri, "rtsp://192.0.2.10:8554/living_h264")
        with self.assertRaises(ONVIFError):
            await camera.create_events_service()
        with self.assertRaises(ONVIFError):
            await camera.create_imaging_service()
        with self.assertRaises(Fault):
            await media.GetSnapshotUri({"ProfileToken": profile.token})
        # SnapshotUri=false makes HA use its FFmpeg fallback, without a broken URI.
        with self.assertLogs("onvif", level="WARNING"):
            self.assertIsNone(await camera.get_snapshot(profile.token))

    async def test_identity_is_distinct_stable_and_accepts_blank_credentials(self):
        identities = []
        for index in (0, 1):
            camera = await self.client(index)
            device = await camera.create_devicemgmt_service()
            identities.append((await device.GetNetworkInterfaces())[0].Info.HwAddress)
        self.assertNotEqual(*identities)
        # A different host port and a process restart must not change identity.
        await self.stop_bridge()
        self.ports = free_ports(3)
        await self.start_bridge()
        camera = await self.client(0, ("placeholder", "placeholder"))
        device = await camera.create_devicemgmt_service()
        self.assertEqual(
            (await device.GetNetworkInterfaces())[0].Info.HwAddress, identities[0]
        )

    async def test_h265_metadata_is_preserved_and_fails_ha_h264_gate(self):
        camera = await self.client(2)
        device = await camera.create_devicemgmt_service()
        self.assertTrue((await device.GetNetworkInterfaces())[0].Enabled)
        media = await camera.create_media_service()
        profile = (await media.GetProfiles())[0]
        self.assertEqual(profile.VideoEncoderConfiguration.Encoding, "H265")
        self.assertEqual(profile.VideoEncoderConfiguration.Resolution.Width, 3840)
        self.assertFalse(
            any(
                p.VideoEncoderConfiguration
                and p.VideoEncoderConfiguration.Encoding == "H264"
                for p in await media.GetProfiles()
            )
        )

    async def test_ptz_and_presets_reach_only_mock_backend(self):
        camera = await self.client()
        ptz = await camera.create_ptz_service()
        await ptz.ContinuousMove(
            {"ProfileToken": "living", "Velocity": {"PanTilt": {"x": -0.5, "y": 0}}}
        )
        async with asyncio.timeout(2):
            while not any(
                query.get("direction") == "left" for _, _, query in self.calls
            ):
                await asyncio.sleep(0.01)
        await ptz.Stop({"ProfileToken": "living", "PanTilt": True, "Zoom": False})
        await ptz.GotoPreset({"ProfileToken": "living", "PresetToken": "1"})
        self.assertTrue(
            any(query.get("direction") == "stop" for _, _, query in self.calls)
        )
        self.assertTrue(
            any(
                path == "/api/xiaomi/presets" and query.get("token") == "1"
                for _, path, query in self.calls
            )
        )
        self.assertTrue(all(query["src"] == "living" for _, _, query in self.calls))

    async def test_discovered_zero_and_sparse_presets_round_trip(self):
        self.preset_list = [
            {"token": "0", "name": "Home"},
            {"token": "7", "name": "Window & door"},
        ]
        camera = await self.client()
        ptz = await camera.create_ptz_service()
        presets = await ptz.GetPresets("living")
        self.assertEqual(
            [(p.token, p.Name) for p in presets],
            [("0", "Home"), ("7", "Window & door")],
        )
        # Use discovered tokens, as HA does, rather than assuming list indexes.
        for preset in presets:
            await ptz.GotoPreset(
                {"ProfileToken": "living", "PresetToken": preset.token}
            )
        sent = [
            q["token"]
            for method, path, q in self.calls
            if method == "POST" and path == "/api/xiaomi/presets"
        ]
        self.assertEqual(sent, ["0", "7"])
        with self.assertRaises(Fault):
            await ptz.GotoPreset({"ProfileToken": "living", "PresetToken": "1"})

    async def test_preset_discovery_failure_and_refresh(self):
        camera = await self.client()
        ptz = await camera.create_ptz_service()
        self.preset_error = True
        with self.assertRaises(Fault):
            await ptz.GetPresets("living")
        self.preset_error = False
        self.preset_list = []
        self.assertEqual(await ptz.GetPresets("living"), [])
        # HA reloads its discovered list on integration setup. A new query must
        # expose favorites created after the previous initialization.
        self.preset_list = [{"token": "7", "name": "Home"}]
        presets = await ptz.GetPresets("living")
        self.assertEqual([p.token for p in presets], ["7"])
        await ptz.GotoPreset(
            {"ProfileToken": "living", "PresetToken": presets[0].token}
        )

    async def test_actual_ha_preset_guard_and_reload(self):
        # Execute the unmodified service method from the pinned HA release.
        # This isolates its real guard behavior without booting HA Core.
        namespace = {
            "Profile": object,
            "LOGGER": logging.getLogger("ha_preset_guard"),
            "PAN_FACTOR": {},
            "TILT_FACTOR": {},
            "ZOOM_FACTOR": {},
            "CONTINUOUS_MOVE": "ContinuousMove",
            "RELATIVE_MOVE": "RelativeMove",
            "ABSOLUTE_MOVE": "AbsoluteMove",
            "GOTOPRESET_MOVE": "GotoPreset",
            "STOP_MOVE": "Stop",
            "ONVIFError": ONVIFError,
            "asyncio": asyncio,
        }
        fixture = Path(__file__).parent / "fixtures" / "ha_perform_ptz.py.txt"
        exec(compile(fixture.read_text(), str(fixture), "exec"), namespace)
        perform = namespace["async_perform_ptz"]
        camera = await self.client()
        ptz = await camera.create_ptz_service()
        device = SimpleNamespace(
            capabilities=SimpleNamespace(ptz=True), device=camera, name="Test camera"
        )
        profile = SimpleNamespace(token="living", ptz=SimpleNamespace(presets=[]))

        async def move(token):
            await perform(
                device,
                profile,
                distance=0.1,
                speed=0.5,
                move_mode="GotoPreset",
                continuous_duration=0.5,
                preset=token,
            )

        self.preset_list = []
        profile.ptz.presets = [p.token for p in await ptz.GetPresets(profile.token)]
        self.calls.clear()
        with self.assertLogs("ha_preset_guard", level="WARNING"):
            await move("0")
        self.assertEqual(
            self.calls, [], "HA must not send a request for an empty preset list"
        )

        self.preset_list = [
            {"token": "0", "name": "Home"},
            {"token": "7", "name": "Window"},
        ]
        # A reload re-queries GetPresets and replaces HA's cached token list.
        profile.ptz.presets = [p.token for p in await ptz.GetPresets(profile.token)]
        self.calls.clear()
        with self.assertLogs("ha_preset_guard", level="WARNING"):
            await move("1")
        self.assertEqual(self.calls, [], "HA must not send an undiscovered token")
        await move("0")
        self.assertTrue(
            any(
                method == "POST" and query.get("token") == "0"
                for method, _, query in self.calls
            )
        )

    async def test_cold_movement_can_exceed_old_two_second_timeout(self):
        self.move_delay = 2.1
        camera = await self.client()
        ptz = await camera.create_ptz_service()
        await ptz.RelativeMove(
            {"ProfileToken": "living", "Translation": {"PanTilt": {"x": 0.1, "y": 0}}}
        )
        async with asyncio.timeout(5):
            await self.move_finished.wait()
        self.assertFalse(
            self.move_connection_closed,
            "Bridge canceled a cold movement after two seconds",
        )
        self.move_delay = 0
        await ptz.Stop({"ProfileToken": "living", "PanTilt": True, "Zoom": False})


if __name__ == "__main__":
    unittest.main()
