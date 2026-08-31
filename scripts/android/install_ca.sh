#!/bin/bash
# Install mitmproxy CA as system cert on rooted Android
# Run once, then reboot device

set -e

CA_PATH="$HOME/.mitmproxy/mitmproxy-ca-cert.cer"
if [ ! -f "$CA_PATH" ]; then
    echo "Run 'mitmproxy' once first to generate CA"
    exit 1
fi

# Get hash for Android cert naming
HASH=$(openssl x509 -inform PEM -subject_hash_old -in "$CA_PATH" | head -1)
CERT_NAME="${HASH}.0"

echo "Installing mitmproxy CA as system cert..."
echo "Hash: $HASH"

# Convert to Android format
openssl x509 -inform PEM -outform DER -in "$CA_PATH" -out "/tmp/$CERT_NAME"

# Push to device
adb push "/tmp/$CERT_NAME" /sdcard/

# Install as system cert (requires root + remount)
adb shell "su -c 'mount -o rw,remount /system'"
adb shell "su -c 'cp /sdcard/$CERT_NAME /system/etc/security/cacerts/'"
adb shell "su -c 'chmod 644 /system/etc/security/cacerts/$CERT_NAME'"
adb shell "su -c 'mount -o ro,remount /system'"

echo "Done! Reboot device for changes to take effect."
echo "Run: adb reboot"
