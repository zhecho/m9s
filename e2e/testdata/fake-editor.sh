#!/bin/sh
# fake-editor.sh: overwrite the draft file with a minimal valid .eml so m9s
# shows the send-modal. $1 is the temp file path passed by m9s launchEditor.
cat > "$1" << 'EOF'
From: e2e@example.com
To: e2e@example.com
Subject: E2E Test Message

This is a test message body written by the e2e fake editor.
EOF
