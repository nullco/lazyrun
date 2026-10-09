#!/usr/bin/env python3
"""Offline installer tests: mock platform/downloads, never contact GitHub."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().with_name("install.sh")
BASE = "https://github.com/nullco/lazyrun/releases"
BINARY = b"#!/bin/sh\necho 'fixture lazyrun'\n"


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="lazyrun-installer-test.")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.assets = self.root / "assets"
        self.assets.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ)
        for key in ("LAZYRUN_VERSION", "LAZYRUN_INSTALL_DIR"):
            self.env.pop(key, None)
        self.env.update(
            HOME=str(self.home),
            TMPDIR=str(self.root),
            PATH=str(self.bin) + ":" + os.environ["PATH"],
            TEST_OS="Linux",
            TEST_ARCH="x86_64",
            TEST_ASSETS=str(self.assets),
            TEST_LOG=str(self.root / "downloads"),
            TEST_LATEST=BASE + "/tag/v0.1.0",
            TEST_DOWNLOAD_FAIL="0",
        )
        self.write_command("uname", "#!/bin/sh\ncase $1 in -s) echo \"$TEST_OS\";; -m) echo \"$TEST_ARCH\";; *) exit 1;; esac\n")
        self.write_command("curl", f"#!{sys.executable}\n" + '''import os
from pathlib import Path
import shutil
import sys
args = sys.argv[1:]
assert args[args.index('--proto') + 1] == '=https'
assert args[args.index('--proto-redir') + 1] == '=https'
url = args[-1]
with open(os.environ['TEST_LOG'], 'a') as log:
    log.write(url + '\\n')
if os.environ['TEST_DOWNLOAD_FAIL'] == '1':
    sys.exit(22)
if url.endswith('/latest'):
    print(os.environ['TEST_LATEST'], end='')
else:
    output = args[args.index('-o') + 1]
    source = Path(os.environ['TEST_ASSETS']) / url.rsplit('/', 1)[-1]
    if not source.is_file():
        sys.exit(22)
    shutil.copyfile(source, output)
''')
        self.fixture("v0.1.0", "amd64")

    def write_command(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def fixture(self, version, arch, symlink=False):
        archive_name = f"lazyrun_{version}_linux_{arch}.tar.gz"
        archive = self.assets / archive_name
        with tarfile.open(archive, "w:gz") as tar:
            entry = tarfile.TarInfo("lazyrun")
            entry.mode = 0o755
            if symlink:
                entry.type = tarfile.SYMTYPE
                entry.linkname = "/bin/sh"
                tar.addfile(entry)
            else:
                entry.size = len(BINARY)
                tar.addfile(entry, io.BytesIO(BINARY))
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        sums = self.assets / f"lazyrun_{version}_SHA256SUMS"
        sums.write_text(f"{digest}  {archive_name}\n" + "0" * 64 + "  unrelated.tar.gz\n")
        return archive, sums

    @property
    def destination(self):
        return Path(self.env.get("LAZYRUN_INSTALL_DIR", str(self.home / ".local/bin"))) / "lazyrun"

    def run_installer(self, success=True, piped=False):
        args = ["/bin/sh"] if piped else ["/bin/sh", str(INSTALLER)]
        result = subprocess.run(args, input=INSTALLER.read_text() if piped else None,
                                env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        self.assertEqual(list(self.root.glob("lazyrun-install.*")), [])
        if self.destination.parent.exists():
            self.assertEqual(list(self.destination.parent.glob(".lazyrun.*")), [])
        return result

    def assert_installed(self):
        self.assertEqual(self.destination.read_bytes(), BINARY)
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o755)

    def test_latest_piped_default_install(self):
        result = self.run_installer(piped=True)
        self.assert_installed()
        self.assertIn("Add this directory to your PATH", result.stdout)
        urls = (self.root / "downloads").read_text().splitlines()
        self.assertEqual(urls[0], BASE + "/latest")
        self.assertIn("/download/v0.1.0/", urls[1])

    def test_pinned_prerelease_arm64_custom_directory(self):
        self.env.update(LAZYRUN_VERSION="v0.2.0-rc.1", TEST_ARCH="aarch64",
                        LAZYRUN_INSTALL_DIR=str(self.home / "custom bin"))
        self.fixture("v0.2.0-rc.1", "arm64")
        self.run_installer()
        self.assert_installed()
        urls = (self.root / "downloads").read_text()
        self.assertNotIn("/latest", urls)
        self.assertIn("v0.2.0-rc.1_linux_arm64", urls)

    def test_architecture_aliases(self):
        for arch, target in [("amd64", "amd64"), ("arm64", "arm64")]:
            with self.subTest(arch=arch):
                self.env["TEST_ARCH"] = arch
                self.fixture("v0.1.0", target)
                self.run_installer()
                self.assert_installed()

    def test_unsupported_platforms(self):
        for system, arch in [("Darwin", "arm64"), ("Linux", "i686"), ("Linux", "riscv64")]:
            with self.subTest(system=system, arch=arch):
                self.env.update(TEST_OS=system, TEST_ARCH=arch)
                self.run_installer(success=False)
                self.assertFalse(self.destination.exists())
                self.assertFalse((self.root / "downloads").exists())

    def test_invalid_versions(self):
        for version in ["dev", "v1.2", "v01.2.3", "v1.2.3-rc.01", "../../bad",
                        "v1.2.3;touch bad", "v0.1.0\nv0.2.0"]:
            with self.subTest(version=version):
                self.env["LAZYRUN_VERSION"] = version
                self.run_installer(success=False)
                self.assertFalse(self.destination.exists())
                self.assertFalse((self.root / "downloads").exists())

    def test_download_failure_preserves_installation(self):
        self.destination.parent.mkdir(parents=True)
        self.destination.write_bytes(b"previous")
        self.env.update(LAZYRUN_VERSION="v0.1.0", TEST_DOWNLOAD_FAIL="1")
        self.run_installer(success=False)
        self.assertEqual(self.destination.read_bytes(), b"previous")

    def test_latest_lookup_failure(self):
        self.env["TEST_DOWNLOAD_FAIL"] = "1"
        result = self.run_installer(success=False)
        self.assertIn("latest stable release", result.stderr)
        self.assertFalse(self.destination.exists())

    def test_unexpected_latest_redirect(self):
        self.env["TEST_LATEST"] = "https://example.com/tag/v0.1.0"
        self.run_installer(success=False)
        self.assertFalse(self.destination.exists())

    def test_corrupt_archive_preserves_installation(self):
        self.destination.parent.mkdir(parents=True)
        self.destination.write_bytes(b"previous")
        archive, _ = self.fixture("v0.1.0", "amd64")
        archive.write_bytes(b"corrupted")
        self.run_installer(success=False)
        self.assertEqual(self.destination.read_bytes(), b"previous")

    def test_bad_manifests(self):
        _, sums = self.fixture("v0.1.0", "amd64")
        valid = sums.read_text()
        for manifest in ["", valid + valid, "x" * 64 + "  lazyrun_v0.1.0_linux_amd64.tar.gz\n"]:
            with self.subTest(manifest=manifest):
                sums.write_text(manifest)
                self.run_installer(success=False)
                self.assertFalse(self.destination.exists())

    def test_archive_symlink_rejected(self):
        self.fixture("v0.1.0", "amd64", symlink=True)
        self.run_installer(success=False)
        self.assertFalse(self.destination.exists())

    def test_upgrade_replaces_binary(self):
        self.destination.parent.mkdir(parents=True)
        self.destination.write_bytes(b"previous")
        self.run_installer()
        self.assert_installed()

    def test_destination_symlink_not_followed(self):
        self.destination.parent.mkdir(parents=True)
        target = self.root / "other-file"
        target.write_bytes(b"leave alone")
        self.destination.symlink_to(target)
        self.run_installer()
        self.assert_installed()
        self.assertFalse(self.destination.is_symlink())
        self.assertEqual(target.read_bytes(), b"leave alone")

    def test_destination_directory_rejected(self):
        self.destination.mkdir(parents=True)
        self.run_installer(success=False)
        self.assertTrue(self.destination.is_dir())

    def test_no_profile_edits(self):
        profile = self.home / ".profile"
        profile.write_text("# existing profile\n")
        self.run_installer()
        self.assertEqual(profile.read_text(), "# existing profile\n")

    def test_truncated_pipe_does_not_install(self):
        script = INSTALLER.read_text().split('    archive="')[0]
        result = subprocess.run(["/bin/sh"], input=script, env=self.env,
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.destination.exists())
        self.assertFalse((self.root / "downloads").exists())


if __name__ == "__main__":
    unittest.main()
