"""Exercise installer transactions in disposable roots; never invokes launchd."""
import os
from pathlib import Path
import plistlib
import re
import subprocess
import shutil
import tempfile
import unittest
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent


class InstallerTests(unittest.TestCase):
    def run_script(self, body):
        with tempfile.TemporaryDirectory(prefix="tenebra-pkg-test-") as temporary:
            root = Path(temporary)
            fake = root / "launchctl"
            fake.write_text('''#!/bin/bash
set -eu
case "$1" in
 print) test -f "$TEST_ROOT/loaded" ;;
 bootout) rm -f "$TEST_ROOT/loaded" ;;
 bootstrap) test ! -f "$TEST_ROOT/fail-bootstrap"; touch "$TEST_ROOT/loaded" ;;
 kickstart) test -f "$TEST_ROOT/loaded" ;;
 *) exit 90 ;;
esac
''')
            fake.chmod(0o755)
            setup = '''
set -euo pipefail
source "$COMMON"
configure_paths "$TEST_ROOT"
ROOT_UID="$(id -u)"
LAUNCHCTL="$TEST_ROOT/launchctl"
root_own() { :; }
mkdir -p "$TEST_ROOT/Applications" "$TEST_ROOT/Library/PrivilegedHelperTools" "$TEST_ROOT/Library/LaunchDaemons" "$TEST_ROOT/Library/Logs"
install_old() {
 mkdir -p "$APP/Contents" "$HELPER" "$LOG_DIR" "$TEST_ROOT/Library/Application Support/Tenebra"
 echo old-app > "$APP/Contents/old"
 for name in "${HELPER_FILES[@]}"; do echo "old-$name" > "$HELPER/$name"; done
 echo preserve-unknown > "$HELPER/custom-file"
 echo old-plist > "$PLIST"
 echo PRIVATE_PROFILE > "$TEST_ROOT/Library/Application Support/Tenebra/profiles.json"
 touch "$TEST_ROOT/loaded"
}
install_new() {
 rm -rf "$APP"
 mkdir -p "$APP/Contents" "$HELPER" "$LOG_DIR"
 echo new-app > "$APP/Contents/new"
 for name in "${HELPER_FILES[@]}"; do echo "new-$name" > "$HELPER/$name"; done
 echo new-plist > "$PLIST"
 chmod 755 "$HELPER/tenebra-healthcheck"
}
'''
            env = dict(os.environ, COMMON=str(HERE / "common.sh"), TEST_ROOT=str(root))
            result = subprocess.run(["/bin/bash", "-c", setup + body], env=env,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_failed_upgrade_restores_exact_payload_and_loaded_state(self):
        self.run_script('''
install_old
prepare_install
test ! -f "$TEST_ROOT/loaded"
install_new
rollback_install
test "$(cat "$APP/Contents/old")" = old-app
test ! -e "$APP/Contents/new"
test "$(cat "$HELPER/tenebra-core")" = old-tenebra-core
test "$(cat "$HELPER/custom-file")" = preserve-unknown
test "$(cat "$PLIST")" = old-plist
test -f "$TEST_ROOT/loaded"
test "$(cat "$TEST_ROOT/Library/Application Support/Tenebra/profiles.json")" = PRIVATE_PROFILE
test ! -e "$BACKUP"
''')

    def test_failed_first_install_removes_only_managed_paths(self):
        self.run_script('''
prepare_install
install_new
echo concurrent-file > "$HELPER/not-our-file"
rollback_install
test ! -e "$APP"
test ! -e "$PLIST"
test ! -e "$HELPER/tenebra-core"
test -f "$HELPER/not-our-file"
test ! -f "$TEST_ROOT/loaded"
test ! -e "$BACKUP"
''')

    def test_failed_upgrade_restores_unloaded_state(self):
        self.run_script('''
install_old
rm "$TEST_ROOT/loaded"
prepare_install
install_new
touch "$TEST_ROOT/loaded"
rollback_install
test ! -f "$TEST_ROOT/loaded"
test -f "$APP/Contents/old"
''')

    def test_symlink_and_stale_state_rejected_before_stop(self):
        self.run_script('''
install_old
mv "$HELPER" "$HELPER-real"
ln -s "$HELPER-real" "$HELPER"
if (prepare_install); then echo accepted-symlink; exit 1; fi
test -f "$TEST_ROOT/loaded"
rm "$HELPER"
mv "$HELPER-real" "$HELPER"
mkdir "$BACKUP"
echo preserve > "$BACKUP/stale"
if (prepare_install); then echo accepted-stale-state; exit 1; fi
test -f "$BACKUP/stale"
test -f "$TEST_ROOT/loaded"
''')

    def test_failed_rollback_keeps_backup(self):
        self.run_script('''
install_old
prepare_install
install_new
touch "$TEST_ROOT/fail-bootstrap"
if rollback_install; then echo hid-rollback-error; exit 1; fi
test -f "$BACKUP/ready"
test -f "$APP/Contents/old"
''')

    def test_unsafe_parents_and_hardlinked_files_rejected(self):
        self.run_script('''
install_old
chmod 777 "$TEST_ROOT/Library/LaunchDaemons"
if (prepare_install); then echo accepted-unsafe-parent; exit 1; fi
test -f "$TEST_ROOT/loaded"
chmod 755 "$TEST_ROOT/Library/LaunchDaemons"
ln "$HELPER/tenebra-core" "$TEST_ROOT/hardlink"
if (prepare_install); then echo accepted-hardlink; exit 1; fi
test -f "$TEST_ROOT/loaded"
test ! -e "$BACKUP"
''')

    def test_loaded_daemon_without_plist_rejected_before_stop(self):
        self.run_script('''
install_old
rm "$PLIST"
if (prepare_install); then echo accepted-unrestorable-state; exit 1; fi
test -f "$TEST_ROOT/loaded"
test ! -e "$BACKUP"
''')

    def test_success_requires_protocol_checker(self):
        self.run_script('''
install_old
prepare_install
install_new
printf '#!/bin/bash\nexit 1\n' > "$HELPER/tenebra-healthcheck"
chmod 755 "$HELPER/tenebra-healthcheck"
HEALTH_ATTEMPTS=1
if finish_install; then echo accepted-unready-daemon; exit 1; fi
test -f "$APP/Contents/old"
test -f "$TEST_ROOT/loaded"
test ! -e "$BACKUP"
prepare_install
install_new
printf '#!/bin/bash\nexit 0\n' > "$HELPER/tenebra-healthcheck"
chmod 755 "$HELPER/tenebra-healthcheck"
finish_install
test -f "$APP/Contents/new"
test -f "$TEST_ROOT/loaded"
test ! -e "$BACKUP"
''')


class BuildValidationTests(unittest.TestCase):
    def test_package_script_archive_has_root_ownership(self):
        with tempfile.TemporaryDirectory(prefix="tenebra-script-archive-") as temporary:
            root = Path(temporary)
            scripts, payload, raw = (root / name for name in ("scripts", "payload", "raw"))
            for directory in (scripts, payload, raw):
                directory.mkdir(mode=0o700)
            for name in ("common.sh", "preinstall", "postinstall"):
                target = scripts / name
                target.write_bytes((HERE / name).read_bytes())
                target.chmod(0o644 if name == "common.sh" else 0o755)
            (payload / "fixture").write_text("payload")
            package = root / "component.pkg"
            subprocess.run(["/usr/bin/pkgbuild", "--root", str(payload), "--scripts", str(scripts),
                            "--ownership", "recommended", "--identifier", "com.tenebra.test.owner",
                            "--version", "1", str(package)], check=True, capture_output=True)
            env = dict(os.environ, NORMALIZER=str(HERE / "normalize-scripts.sh"),
                       PACKAGE=str(package), SCRIPTS=str(scripts))
            result = subprocess.run(["/bin/bash", "-c", 'set -eu; source "$NORMALIZER"; normalize_scripts_archive "$PACKAGE" "$SCRIPTS"'],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            subprocess.run(["/usr/bin/xar", "-xf", str(package), "-C", str(raw), "Scripts"], check=True)
            # cpio listing uses numeric uid/gid, so it does not depend on user names.
            data = subprocess.run(["/usr/bin/gzip", "-dc", str(raw / "Scripts")], check=True, capture_output=True).stdout
            listing = subprocess.run(["/usr/bin/cpio", "-itv", "--numeric-uid-gid"], input=data, capture_output=True)
            self.assertEqual(listing.returncode, 0, listing.stderr.decode())
            for line in listing.stdout.decode().splitlines():
                self.assertRegex(line, r"^\S+\s+\d+\s+0\s+0\s+")

    def check_rejection(self, change, message):
        with tempfile.TemporaryDirectory(prefix="tenebra-build-test-") as temporary:
            root = Path(temporary)
            app = root / "Tenebra.app"
            (app / "Contents/MacOS").mkdir(parents=True)
            resources = app / "Contents/Resources/resources"
            resources.mkdir(parents=True)
            for path in [app / "Contents/MacOS/tenebra-desktop", app / "Contents/MacOS/tenebra-core", resources / "sing-box"]:
                path.write_text("fixture")
                path.chmod(0o755)
            for name in ("geoip-ru.srs", "geosite-ru.srs", "geosite-ads.srs"):
                (resources / name).write_text("fixture")
            (resources / "THIRD-PARTY-NOTICES.md").write_text("fixture")
            source = (HERE.parents[1] / "core/buildinfo/buildinfo.go").read_text()
            version = re.search(r'^const Version = "([^"]+)"$', source, re.MULTILINE).group(1)
            (app / "Contents/Info.plist").write_bytes(plistlib.dumps({
                "CFBundleIdentifier": "com.tenebra.desktop", "CFBundleShortVersionString": version,
                "CFBundleVersion": version, "CFBundleExecutable": "tenebra-desktop"}))
            change(app, resources)
            result = subprocess.run(["/bin/bash", str(HERE.parents[1] / "scripts/macos/build-pkg.sh"),
                                     "--app", str(app), "--output", str(root / "output.pkg")],
                                    capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(message, result.stdout + result.stderr)
            self.assertFalse((root / "output.pkg").exists())

    def test_missing_ruleset_rejected(self):
        self.check_rejection(lambda app, res: (res / "geosite-ads.srs").unlink(), "missing payload")

    def test_symlink_payload_rejected(self):
        def change(app, res):
            (res / "geoip-ru.srs").unlink()
            (res / "geoip-ru.srs").symlink_to("/etc/hosts")
        self.check_rejection(change, "symbolic link or special file")

    def test_wrong_bundle_version_rejected(self):
        def change(app, res):
            path = app / "Contents/Info.plist"
            info = plistlib.loads(path.read_bytes())
            info["CFBundleShortVersionString"] = "0.6.0"
            path.write_bytes(plistlib.dumps(info))
        self.check_rejection(change, "bundle version does not match core")

    def test_wrong_bundle_build_version_rejected(self):
        def change(app, res):
            path = app / "Contents/Info.plist"
            info = plistlib.loads(path.read_bytes())
            info["CFBundleVersion"] = "0.6.0"
            path.write_bytes(plistlib.dumps(info))
        self.check_rejection(change, "bundle version does not match core")


class PackagePortabilityTests(unittest.TestCase):
    """Run production validators; only Apple-tool/Go boundaries are replaced."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="tenebra-portable-pkg-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "core/buildinfo").mkdir(parents=True)
        (self.root / "core/buildinfo/buildinfo.go").write_text('const Version = "0.6.2"\n')
        (self.root / ".go-version").write_text("1.27.3\n")
        self.go = self.root / "go"
        self.go.write_text('''#!/bin/bash
set -eu
case "$*" in
 version) printf 'go version %s %s\\n' "${TEST_GO_VERSION:-go1.27.3}" "${TEST_GO_HOST:-darwin/arm64}" ;;
 'version -m '*) printf '%s: %s\\n' "$3" "$(head -n 1 "$3" | cut -d ' ' -f 2)"; tail -n +2 "$3" ;;
 *) exit 91 ;;
esac
''')
        self.go.chmod(0o755)

    def run_helpers(self, body, *, success=True, env=None):
        setup = '''set -euo pipefail
source "$BUILDER"
REPO_ROOT="$FIXTURE"
GO_TOOL="$TEST_GO"
read_source_metadata
configure_architecture "${TEST_ARCH:-arm64}"
'''
        result = subprocess.run(["/bin/bash", "-c", setup + body], text=True, capture_output=True,
                                env={**os.environ, "BUILDER": str(HERE.parents[1] / "scripts/macos/build-pkg.sh"),
                                     "FIXTURE": str(self.root), "TEST_GO": str(self.go), **(env or {})})
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def write_metadata(self, arch, *, compiler="go1.27.3", goarch=None, goos="darwin",
                       module="github.com/Divaaaan/tenebra/cmd/tenebra-core"):
        (self.root / arch).write_text(
            f"fixture: {compiler}\n\tpath\t{module}\n\tmod\tgithub.com/Divaaaan/tenebra\tv0.6.2\t\n"
            f"\tbuild\tGOOS={goos}\n\tbuild\tGOARCH={goarch or ('amd64' if arch == 'x86_64' else arch)}\n")

    def test_source_version_and_changed_go_pin_are_read_from_fixture(self):
        result = self.run_helpers('printf "%s|%s\\n" "$VERSION" "$EXPECTED_GO"')
        self.assertEqual(result.stdout.strip(), "0.6.2|go1.27.3")

    def test_invalid_go_pin_is_rejected(self):
        (self.root / ".go-version").write_text("1.27.3 malicious\n")
        self.assertIn(".go-version", self.run_helpers(':', success=False).stderr)

    def test_exact_selected_go_accepts_both_native_mac_hosts(self):
        for host in ("darwin/arm64", "darwin/amd64"):
            with self.subTest(host=host):
                self.run_helpers('validate_go_tool', env={"TEST_GO_HOST": host})

    def test_wrong_selected_compiler_or_non_mac_host_is_rejected(self):
        for env in ({"TEST_GO_VERSION": "go1.27.2"}, {"TEST_GO_VERSION": "go1.27.30"},
                    {"TEST_GO_HOST": "linux/arm64"}):
            with self.subTest(env=env):
                result = self.run_helpers('validate_go_tool', env=env, success=False)
                self.assertIn("Go go1.27.3", result.stderr)

    def core_validator(self, *, success=True):
        return self.run_helpers('''
# A universal binary must be inspected as separate, non-executable thin copies.
lipo() {
  case "$1" in
    -archs) echo "arm64 x86_64" ;;
    "$FIXTURE/core") test "$2" = -thin; test "$4" = -output; cp "$FIXTURE/$3" "$5" ;;
    *) return 92 ;;
  esac
}
validate_core_metadata "$FIXTURE/core" "$FIXTURE"
''', success=success, env={"TEST_ARCH": "universal"})

    def test_universal_core_validates_both_go_metadata_slices(self):
        self.write_metadata("arm64")
        self.write_metadata("x86_64")
        self.core_validator()

    def test_wrong_second_core_slice_metadata_is_rejected(self):
        self.write_metadata("arm64")
        for kwargs in ({"compiler": "go1.27.30"}, {"goarch": "arm64"},
                       {"goos": "linux"}, {"module": "evil/tenebra-core"}):
            with self.subTest(kwargs=kwargs):
                self.write_metadata("x86_64", **kwargs)
                result = self.core_validator(success=False)
                self.assertIn("core", result.stderr)
                self.assertIn("x86_64", result.stderr)

    def macho_validator(self, *, success=True, env=None):
        return self.run_helpers('''
lipo() { test "$1" = -archs; echo "${TEST_SLICES:-arm64 x86_64}"; }
codesign() {
  case "$1" in
    --verify) return 0 ;;
    -dv) local signature=adhoc; if [[ "$3" = x86_64 ]]; then signature="${TEST_X86_SIGNATURE:-adhoc}"; fi
         echo "Signature=$signature" >&2 ;;
    *) return 93 ;;
  esac
}
xcrun() {
  test "$1" = vtool; test "$2" = -arch; test "$4" = -show-build
  local minos=12.0
  if [[ "$3" = x86_64 ]]; then minos="${TEST_X86_MINOS:-15.0}"; fi
  printf 'Load command 6\\n cmd LC_BUILD_VERSION\\n platform %s\\n minos %s\\n sdk 15.0\\n' "${TEST_PLATFORM:-MACOS}" "$minos"
}
validate_macho "$FIXTURE/go"
''', success=success, env={"TEST_ARCH": "universal", **(env or {})})

    def test_universal_macho_accepts_supported_minima_for_all_slices(self):
        self.macho_validator()

    def test_universal_missing_architecture_is_rejected(self):
        result = self.macho_validator(success=False, env={"TEST_SLICES": "arm64"})
        self.assertIn("x86_64", result.stderr)

    def test_universal_later_minimum_in_second_slice_is_rejected(self):
        for version in ("15.0.1", "15.1", "16.0", "nonsense"):
            with self.subTest(version=version):
                result = self.macho_validator(success=False, env={"TEST_X86_MINOS": version})
                self.assertIn("x86_64", result.stderr)

    def test_non_macos_slice_is_rejected(self):
        result = self.macho_validator(success=False, env={"TEST_PLATFORM": "IOS"})
        self.assertIn("macOS", result.stderr)

    def test_non_adhoc_second_slice_is_rejected(self):
        result = self.macho_validator(success=False, env={"TEST_X86_SIGNATURE": "Developer ID"})
        self.assertIn("ad-hoc x86_64", result.stderr)

    def test_distribution_uses_selected_architecture_and_future_version(self):
        for arch, expected in (("arm64", "arm64"), ("universal", "arm64,x86_64")):
            with self.subTest(arch=arch):
                output = self.root / "Distribution.xml"
                self.run_helpers('render_distribution "$TEMPLATE" "$FIXTURE/Distribution.xml"',
                                 env={"TEST_ARCH": arch, "TEMPLATE": str(HERE / "Distribution.xml")})
                xml = ET.parse(output).getroot()
                self.assertEqual(xml.find("options").attrib["hostArchitectures"], expected)
                self.assertEqual(xml.find("pkg-ref").attrib["version"], "0.6.2")
                self.assertEqual(xml.find("allowed-os-versions/os-version").attrib["min"], "15.0")

    def test_invalid_architecture_is_rejected(self):
        self.assertIn("--arch", self.run_helpers(':', success=False, env={"TEST_ARCH": "amd64"}).stderr)

    def checker_builder(self, *, success=True, env=None):
        self.go.write_text('''#!/bin/bash
set -eu
test "$1" = build
test "$GOTOOLCHAIN" = local
test "$GOOS" = darwin
test "$CGO_ENABLED" = 0
while [[ "$1" != -o ]]; do shift; done
printf '%s\\n' "$GOARCH" > "$2"
''')
        return self.run_helpers('''
lipo() {
  case "$1" in
    -create) test "$(cat "$2")" = arm64; test "$(cat "$3")" = amd64; test "$4" = -output; cat "$2" "$3" > "$5" ;;
    -archs) echo "arm64 x86_64" ;;
    *) return 95 ;;
  esac
}
codesign() { [[ "$1" != -dv ]] || echo Signature=adhoc >&2; }
xcrun() {
  test "$1" = vtool; test "$2" = -arch; test "$4" = -show-build
  printf 'platform MACOS\\nminos %s\\n' "${TEST_CHECKER_MINOS:-12.0}"
}
build_healthcheck "$FIXTURE/checker" "$FIXTURE"
test -x "$FIXTURE/checker"
''', success=success, env={"TEST_ARCH": "universal", **(env or {})})

    def test_checker_builds_both_targets_and_validates_result(self):
        self.checker_builder()
        self.assertEqual((self.root / "checker").read_text(), "arm64\namd64\n")

    def test_checker_later_mac_os_minimum_is_rejected(self):
        result = self.checker_builder(success=False, env={"TEST_CHECKER_MINOS": "16.0"})
        self.assertIn("later than declared 15.0", result.stderr)

    def test_selected_go_and_checker_ignore_inherited_foreign_workspace(self):
        native_go = shutil.which("go")
        if not native_go or not shutil.which("lipo"):
            self.skipTest("native Go and Apple command-line tools are required")
        version = subprocess.run([native_go, "version"], check=True, capture_output=True,
                                 text=True, env=dict(os.environ, GOTOOLCHAIN="local", GOWORK="off")).stdout.split()[2]
        (self.root / ".go-version").write_text(version.removeprefix("go") + "\n")
        (self.root / "go.mod").write_text("module github.com/Divaaaan/tenebra\n\ngo 1.24.0\n")
        source = self.root / "cmd/tenebra-healthcheck"
        source.mkdir(parents=True)
        (source / "main.go").write_text("package main\nfunc main() {}\n")
        foreign = self.root / "foreign"
        (foreign / "other").mkdir(parents=True)
        (foreign / "other/go.mod").write_text("module example.com/foreign\n\ngo 1.24.0\n")
        workspace = foreign / "go.work"
        workspace.write_text("go 1.24.0\n\nuse ./other\n")
        env = {"TEST_GO": native_go, "GOWORK": str(workspace)}
        with self.subTest(boundary="checker build"):
            self.run_helpers('build_healthcheck "$FIXTURE/checker" "$FIXTURE"', env=env)
            self.assertTrue((self.root / "checker").is_file())
        with self.subTest(boundary="selected compiler environment"):
            result = self.run_helpers('cd "$REPO_ROOT"; validate_go_tool; "$GO_TOOL" list -m', env=env)
            self.assertEqual(result.stdout.strip(), "github.com/Divaaaan/tenebra")

    def test_real_macho_and_go_metadata_validators(self):
        """Tiny temporary Go fixture only; no product/app build or binary execution."""
        native_go = shutil.which("go")
        if not native_go or not shutil.which("lipo"):
            self.skipTest("native Go and Apple command-line tools are required")
        version = subprocess.run([native_go, "version"], check=True, capture_output=True,
                                 text=True, env=dict(os.environ, GOTOOLCHAIN="local")).stdout.split()[2]
        (self.root / ".go-version").write_text(version.removeprefix("go") + "\n")
        (self.root / "go.mod").write_text("module github.com/Divaaaan/tenebra\n\ngo 1.24.0\n")
        source = self.root / "cmd/tenebra-core"
        source.mkdir(parents=True)
        (source / "main.go").write_text("package main\nfunc main() {}\n")
        for arch in ("arm64", "amd64"):
            subprocess.run([native_go, "build", "-buildvcs=false", "-o", str(self.root / arch),
                            "./cmd/tenebra-core"], cwd=self.root, check=True, capture_output=True,
                           env=dict(os.environ, GOTOOLCHAIN="local", GOOS="darwin", GOARCH=arch,
                                    CGO_ENABLED="0", GOFLAGS="-p=4", GOMAXPROCS="4"))
        subprocess.run(["lipo", "-create", str(self.root / "arm64"), str(self.root / "amd64"),
                        "-output", str(self.root / "core-fat")], check=True, capture_output=True)
        for binary, mode in (("arm64", "arm64"), ("core-fat", "universal")):
            with self.subTest(mode=mode):
                subprocess.run(["codesign", "--force", "--sign", "-", "--timestamp=none",
                                str(self.root / binary)], check=True, capture_output=True)
                self.run_helpers('validate_go_tool; validate_macho "$FIXTURE/$BINARY"; '
                                 'validate_core_metadata "$FIXTURE/$BINARY" "$FIXTURE"',
                                 env={"TEST_ARCH": mode, "TEST_GO": native_go, "BINARY": binary})


if __name__ == "__main__":
    unittest.main()
