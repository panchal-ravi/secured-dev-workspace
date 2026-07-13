#!/bin/bash
# Builds SecuredWS.app from the AppleScript shim + bundled bash logic, registers
# the secured-ws:// URL scheme in its Info.plist, and emits a zip the portal
# serves for download. Run on macOS (uses osacompile / PlistBuddy / ditto).
#
#   ./build.sh
#
# Output: portal/backend/helper-dist/SecuredWS-macos.zip (served at /helper/…).
# This dir is a sibling of ./web so the frontend build never deletes it.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out_dir="$here/../../backend/helper-dist"
app="$here/build/SecuredWS.app"

rm -rf "$here/build"
mkdir -p "$here/build" "$out_dir"

# 1. Compile the applet (gives us a real .app with the GetURL-capable runtime).
osacompile -o "$app" "$here/secured-ws.applescript"

# 2. Bundle the logic script alongside the compiled handler.
cp "$here/secured-ws.sh" "$app/Contents/Resources/secured-ws.sh"
chmod 755 "$app/Contents/Resources/secured-ws.sh"

# 3. Declare the secured-ws:// scheme + a stable bundle id so LaunchServices
#    registers us as its handler.
plist="$app/Contents/Info.plist"
pb() { /usr/libexec/PlistBuddy -c "$1" "$plist"; }
pb "Set :CFBundleIdentifier dev.secured-workspace.helper" 2>/dev/null \
  || pb "Add :CFBundleIdentifier string dev.secured-workspace.helper"
pb "Add :CFBundleURLTypes array" 2>/dev/null || true
pb "Add :CFBundleURLTypes:0 dict" 2>/dev/null || true
pb "Add :CFBundleURLTypes:0:CFBundleURLName string Secured Workspace" 2>/dev/null || true
pb "Add :CFBundleURLTypes:0:CFBundleURLSchemes array" 2>/dev/null || true
pb "Add :CFBundleURLTypes:0:CFBundleURLSchemes:0 string secured-ws" 2>/dev/null || true

# 4. Re-sign ad-hoc. osacompile signs the applet, but the PlistBuddy edits and the
#    copied-in script invalidate that signature ("Info.plist=not bound"). On Apple
#    Silicon a quarantined app with a broken signature is reported as "damaged and
#    can't be opened", so we must re-seal the whole bundle as the LAST step. Ad-hoc
#    (-s -) is enough for a PoC; the developer still clears quarantine on first open.
codesign --force --deep --sign - "$app"
codesign -v "$app"   # fail the build if it didn't seal cleanly

# 5. Zip it with `zip -X` — NOT ditto. ditto externalizes each file's resource
#    fork / provenance xattr as an AppleDouble `._` member (14 of them here); a
#    developer who expands the download with command-line `unzip` (not Finder's
#    Archive Utility) gets those `._` files written into the bundle, which breaks
#    the code seal ("a sealed resource is missing or invalid") and macOS then
#    silently refuses to launch the app from a secured-ws:// URL — the click does
#    nothing. `zip -r -X` stores no AppleDouble/__MACOSX, so any unzip tool yields
#    an intact, correctly-sealed bundle. (-X drops the metadata; the ad-hoc seal
#    lives in the Mach-O + _CodeSignature file, both preserved byte-for-byte.)
rm -f "$out_dir/SecuredWS-macos.zip"
( cd "$here/build" && zip -r -X -q "$out_dir/SecuredWS-macos.zip" SecuredWS.app )

echo "built: $out_dir/SecuredWS-macos.zip"
