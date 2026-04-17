#!/bin/bash

DB_PATH="./meshcore-data/meshcore.db"
OUTPUT_DIR="./decrypted_channels"
mkdir -p "$OUTPUT_DIR"

echo "Extracting channels from channel-rainbow.json and config.json..."

# Get channels from channel-rainbow.json (keys are channel names with #)
CHANNELS=$(jq -r 'keys[]' channel-rainbow.json)

# Also add channels from config.json channelKeys (in case there are unique ones)
CHANNELS="$CHANNELS"$'\n'$(jq -r '.channelKeys | keys[]' config.json)

# Remove duplicates and sort
CHANNELS=$(echo "$CHANNELS" | sort -u | grep -v '^$')

echo "Found $(echo "$CHANNELS" | wc -l) unique channels"
echo ""

# Decrypt each channel to JSON
for channel in $CHANNELS; do
  echo "Decrypting $channel..."
  clean_name="${channel//#/}"
  corescope-decrypt --channel "$channel" --db "$DB_PATH" --format json > "$OUTPUT_DIR/${clean_name}.json" 2>/dev/null || echo "  ⚠️  No messages found or error"
done

echo ""
echo "✅ Decryption complete! Files saved to $OUTPUT_DIR/"
