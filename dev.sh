#!/bin/sh
# dev.sh: rebuild rexdiff and (re)open it in the rexreview-test session next
# to an "agent" pane, recreating whatever is missing. CONFIG (settings as
# JSON) and ARGS (extra flags) pass through to rexdiff.
set -e
BIN=/tmp/rexdiff-dev/rexdiff
cd "$(dirname "$0")" && go build -o $BIN ./cmd/rexdiff
export PATH="${REX_BIN_DIR:+$REX_BIN_DIR:}$PATH"
S=rexreview-test; R=${REXDIFF_DEMO:-/tmp/rexreview-demo}
has() { rex block inspect "$1" -s $S --json >/dev/null 2>&1; }
rex ls | grep -q "^$S " || rex new $S --cwd $R -- fish >/dev/null
has agent || { rex run -s $S --label agent --cwd $R -- fish >/dev/null; sleep 1; }
# close every pane labelled rexdiff, by id
for id in $(rex api call -s $S session.list_blocks '{}' | python3 -c 'import json,sys;[print(b["block_id"]) for b in json.load(sys.stdin)["blocks"] if b.get("label")=="rexdiff"]'); do
  rex api call -s $S block.close "{\"block_id\":\"$id\"}" >/dev/null 2>&1 || true
done
sleep 0.5
rex split -s $S -b agent --label rexdiff --ratio 70 --cwd $R -- $BIN -config "${CONFIG:-}" -log /tmp/rexdiff.log --agent agent $ARGS >/dev/null
sleep 0.5
rex block call -s $S -b rexdiff resize "{\"columns\":${COLS:-170},\"rows\":${ROWS:-30}}" >/dev/null
sleep 1.5
echo "agent: $(has agent && echo ok) rexdiff: $(has rexdiff && echo ok)"
