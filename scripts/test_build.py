import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock


loader = importlib.machinery.SourceFileLoader("release_builder", str(Path(__file__).with_name("build")))
spec = importlib.util.spec_from_loader(loader.name, loader)
builder = importlib.util.module_from_spec(spec)
loader.exec_module(builder)

VALUES = {"version": "v1.2.3", "commit": "a" * 40, "date": "2026-10-17T12:00:00Z"}


def executable_header(platform):
    goos, goarch = platform
    data = bytearray(128)
    if goos == "darwin":
        data[:4] = b"\xcf\xfa\xed\xfe"
        struct.pack_into("<I", data, 4, {"amd64": 0x1000007, "arm64": 0x100000C}[goarch])
    elif goos == "linux":
        data[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<H", data, 18, {"amd64": 62, "arm64": 183}[goarch])
    else:
        data[:2] = b"MZ"
        struct.pack_into("<I", data, 60, 64)
        data[64:68] = b"PE\x00\x00"
        struct.pack_into("<H", data, 68, {"amd64": 0x8664, "arm64": 0xAA64}[goarch])
    return bytes(data)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.destination = self.root / "dist"
        self.patch(mock.patch.object(builder, "ROOT", self.root))
        for name in ("README.md", "LICENSE", "THIRD_PARTY_NOTICES", "CONTRIBUTING.md", "SECURITY.md", "docs/authentication.md", "docs/nested/automation.md", "skills/expensify/SKILL.md"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"Release documentation: {name}\n", encoding="utf-8")
        self.patch(mock.patch.object(builder, "go_env", side_effect=lambda name: {"GOHOSTOS": "linux", "GOHOSTARCH": "amd64"}[name]))
        self.build = self.patch(mock.patch.object(builder, "build", side_effect=self.fake_build))
        self.run = self.patch(mock.patch.object(builder.subprocess, "check_output", side_effect=lambda args, **kwargs: f"# {args[-1]} completion\n".encode()))

    def patch(self, patcher):
        result = patcher.start()
        self.addCleanup(patcher.stop)
        return result

    def fake_build(self, destination, values, platform):
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(executable_header(platform))

    def release(self, destination=None, values=None):
        with contextlib.redirect_stdout(io.StringIO()):
            builder.release(values or VALUES, destination or self.destination)

    def verify(self):
        with contextlib.redirect_stdout(io.StringIO()):
            builder.verify(self.destination)

    def rewrite_archive(self, platform, transform):
        archive = self.destination / builder.archive_name(VALUES["version"], platform)
        contents = builder.archive_contents(archive)
        transform(contents)
        files = []
        with tempfile.TemporaryDirectory(dir=self.root) as temporary:
            for name, data in contents.items():
                path = Path(temporary) / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
                files.append((path, name))
            builder.write_archive(archive, files, builder.archive_time(VALUES))
        checksums = self.destination / "checksums.txt"
        lines = checksums.read_text().splitlines()
        checksums.write_text("".join(f"{builder.digest(archive)}  {archive.name}\n" if line.endswith(f"  {archive.name}") else line + "\n" for line in lines))

    def test_release_contains_six_correct_binaries_offline_docs_and_completions(self):
        self.release()
        self.verify()
        expected = {builder.archive_name(VALUES["version"], platform) for platform in builder.PLATFORMS}
        self.assertEqual({path.name for path in self.destination.iterdir()}, expected | {"checksums.txt"})
        self.assertEqual(self.build.call_count, 6)
        self.assertEqual({call.args[2] for call in self.build.call_args_list}, set(builder.PLATFORMS))
        for call in self.run.call_args_list:
            self.assertEqual(call.args[0][1], "completion")
            self.assertEqual(Path(call.args[0][0]).parent.name, "host")
        for platform in builder.PLATFORMS:
            with self.subTest(platform=platform):
                contents = builder.archive_contents(self.destination / builder.archive_name(VALUES["version"], platform))
                for name in ("docs/nested/automation.md", "LICENSE", "THIRD_PARTY_NOTICES", "skills/expensify/SKILL.md"):
                    self.assertEqual(contents[name], (self.root / name).read_bytes())
                self.assertEqual(json.loads(contents["release.json"]), {**VALUES, "goos": platform[0], "goarch": platform[1]})
                for shell, name in builder.COMPLETIONS.items():
                    self.assertEqual(contents[name], f"# {shell} completion\n".encode())

    def test_documentation_with_windows_line_endings_is_preserved(self):
        document = self.root / "docs" / "authentication.md"
        document.write_bytes(b"# Authentication\r\n\r\nPrivate credentials.\r\n")
        self.release()
        self.verify()
        for platform in builder.PLATFORMS:
            contents = builder.archive_contents(self.destination / builder.archive_name(VALUES["version"], platform))
            self.assertEqual(contents["docs/authentication.md"], document.read_bytes())

    def test_archive_checksums_do_not_depend_on_source_mtime_or_work_directory(self):
        self.release()
        original = (self.destination / "checksums.txt").read_bytes()
        for path, _ in builder.common_files():
            os.utime(path, (1234567890, 1234567890))
        other = self.root / "other-release"
        self.release(other)
        self.assertEqual(original, (other / "checksums.txt").read_bytes())

    def test_cross_build_failure_preserves_published_release_and_cleans_private_stage(self):
        self.release()
        original = {path.name: path.read_bytes() for path in self.destination.iterdir()}
        def fail_on_windows(destination, values, platform):
            if platform == ("windows", "amd64"):
                raise subprocess.CalledProcessError(1, ["go", "build"])
            self.fake_build(destination, values, platform)
        self.build.side_effect = fail_on_windows
        with self.assertRaises(subprocess.CalledProcessError):
            self.release()
        self.assertEqual(original, {path.name: path.read_bytes() for path in self.destination.iterdir()})
        self.assertEqual(list(self.root.glob(".expensify-release-*")), [])

    def test_completion_failure_does_not_publish_partial_release(self):
        self.run.side_effect = subprocess.CalledProcessError(1, ["expensify", "completion"])
        with self.assertRaises(subprocess.CalledProcessError):
            self.release()
        self.assertFalse(self.destination.exists())
        self.assertEqual(list(self.root.glob(".expensify-release-*")), [])

    def test_unrelated_output_and_prior_versions_are_preserved(self):
        self.destination.mkdir()
        unexpected = self.destination / "notes.txt"
        unexpected.write_text("Do not delete this.")
        with self.assertRaisesRegex(ValueError, "Unexpected release output"):
            self.release()
        self.assertEqual(unexpected.read_text(), "Do not delete this.")
        unexpected.unlink()
        self.release()
        with self.assertRaisesRegex(ValueError, "Unexpected release output"):
            self.release(values={**VALUES, "version": "v2.0.0"})
        self.verify()

    @unittest.skipIf(os.name == "nt", "Windows symlinks require a separate OS privilege")
    def test_symlink_destination_and_archive_are_rejected(self):
        real = self.root / "real"
        real.mkdir()
        self.destination.symlink_to(real, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.release()
        self.destination.unlink()
        self.release()
        archive = next(path for path in self.destination.iterdir() if path.suffix == ".zip")
        saved = self.root / "saved.zip"
        archive.rename(saved)
        archive.symlink_to(saved)
        with self.assertRaisesRegex(ValueError, "Unexpected release output"):
            self.verify()
        self.assertTrue(saved.is_file())

    def test_missing_license_blocks_packaging(self):
        (self.root / "LICENSE").unlink()
        with self.assertRaisesRegex(ValueError, "LICENSE"):
            self.release()
        self.assertFalse(self.destination.exists())

    def test_verifier_rejects_tampered_archive(self):
        self.release()
        archive = next(path for path in self.destination.iterdir() if path.suffix == ".zip")
        with archive.open("ab") as output:
            output.write(b"changed")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.verify()

    def test_verifier_rejects_missing_archive_checksum_and_extra_files(self):
        self.release()
        checksum_path = self.destination / "checksums.txt"
        checksums = checksum_path.read_text()
        checksum_path.write_text("\n".join(checksums.splitlines()[:-1]) + "\n")
        with self.assertRaisesRegex(ValueError, "exactly six"):
            self.verify()
        checksum_path.write_text(checksums)
        (self.destination / "stale.zip").write_bytes(b"old version")
        with self.assertRaisesRegex(ValueError, "Unexpected release output"):
            self.verify()

    def test_verifier_rejects_wrong_architecture_even_with_valid_checksum(self):
        self.release()
        platform = "linux", "arm64"
        self.rewrite_archive(platform, lambda contents: contents.update(expensify=executable_header(("linux", "amd64"))))
        with self.assertRaisesRegex(ValueError, "architecture"):
            self.verify()

    def test_verifier_rejects_inconsistent_release_metadata(self):
        self.release()
        def change_commit(contents):
            details = json.loads(contents["release.json"])
            details["commit"] = "b" * 40
            contents["release.json"] = json.dumps(details).encode()
        self.rewrite_archive(("linux", "amd64"), change_commit)
        with self.assertRaisesRegex(ValueError, "inconsistent release metadata"):
            self.verify()

    def test_verifier_rejects_accidental_private_archive_content(self):
        self.release()
        self.rewrite_archive(("windows", "arm64"), lambda contents: contents.update({"credentials.json": b"private"}))
        with self.assertRaisesRegex(ValueError, "Unexpected or missing archive contents"):
            self.verify()


class BuildMetadataTests(unittest.TestCase):
    def test_build_metadata_and_cross_compilation_flags_are_passed_without_a_shell(self):
        with tempfile.TemporaryDirectory() as temporary:
            with mock.patch.dict(os.environ, {"GOAMD64": "v4", "GOARM64": "v9.5"}), mock.patch.object(builder.subprocess, "run") as run:
                builder.build(Path(temporary) / "expensify.exe", VALUES, ("windows", "arm64"))
        call = run.call_args
        self.assertEqual(call.kwargs["env"]["CGO_ENABLED"], "0")
        self.assertEqual(call.kwargs["env"]["GOOS"], "windows")
        self.assertEqual(call.kwargs["env"]["GOARCH"], "arm64")
        self.assertEqual(call.kwargs["env"]["GOAMD64"], "v1")
        self.assertEqual(call.kwargs["env"]["GOARM64"], "v8.0")
        self.assertNotIn("shell", call.kwargs)
        args = call.args[0]
        self.assertIn("-mod=readonly", args)
        self.assertIn("-trimpath", args)
        self.assertIn("-buildvcs=false", args)
        flags = args[args.index("-ldflags") + 1]
        for key, value in VALUES.items():
            self.assertIn(f"-X=main.{key}={value}", flags.split())

    def test_unsafe_metadata_is_rejected(self):
        for name in ("VERSION", "COMMIT", "BUILD_DATE"):
            with self.subTest(name=name):
                environment = {"VERSION": "v1.2.3", "COMMIT": "abc123", "BUILD_DATE": VALUES["date"], name: "$(touch unsafe)"}
                with mock.patch.dict(os.environ, environment), self.assertRaisesRegex(ValueError, "Invalid"):
                    builder.metadata()

    def test_release_dates_require_timezone_and_archive_supported_range(self):
        for date in ("2026-10-17T12:00:00", "invalid", "1900-01-01T00:00:00Z", "2200-01-01T00:00:00Z"):
            with self.subTest(date=date), self.assertRaisesRegex(ValueError, "BUILD_DATE"):
                builder.archive_time({**VALUES, "date": date})
        self.assertEqual(builder.archive_time({**VALUES, "date": "unknown"}), 0)


if __name__ == "__main__":
    unittest.main()
