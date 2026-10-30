#!/bin/sh
# e2e.sh runs the rexdiff of this tree in a real Rex. It walks through each
# feature of .doc/user.md, then through the edge cases.
#
#	REXDIFF_REX=$(which rex) test/e2e.sh
#
# It needs a running Rex server, go, git and jq. It kills only its own
# session. REXDIFF_E2E_KEEP=1 keeps the files, such as the debug log.
#
# The install, the toggle key and U change the Rex config or need a
# release. So the Go tests in test/ cover them.
set -eu

: "${REXDIFF_REX:?set REXDIFF_REX to the path of rex}"
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/rexdiff-e2e.XXXXXX")
work=$(cd "$work" && pwd -P) # without links, the paths match what git reports
session=rexdiff-e2e-$$
wait_seconds=20

rex() { "$REXDIFF_REX" --autostart=false "$@"; }

cleanup() {
	if [ -n "${events_pid:-}" ]; then kill "$events_pid" 2>/dev/null || true; fi
	rex kill "$session" > /dev/null 2>&1 || true
	if [ -n "${REXDIFF_E2E_KEEP:-}" ]; then echo "kept $work"; else rm -rf "$work"; fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

n=0
what=""
screen=""

step() {
	n=$((n + 1))
	what=$*
	printf '%3d %s\n' "$n" "$what"
}

fail() {
	printf 'FAIL at step %d, %s: %s\n' "$n" "$what" "$*" >&2
	printf '%s\n' "The block shows:" "$screen" >&2
	exit 1
}

# poll WHY CMD...
poll() {
	why=$1
	shift
	end=$(($(date +%s) + wait_seconds))
	until "$@"; do
		[ "$(date +%s)" -lt "$end" ] || fail "$why"
		sleep 0.05
	done
}

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_ATTR_NOSYSTEM=1 GIT_TEMPLATE_DIR=
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# numbered WORD FIRST LAST
numbered() { awk -v w="$1" -v a="$2" -v b="$3" 'BEGIN { for (i = a; i <= b; i++) print w, i }'; }

step "build rexdiff"
bin=$work/rexdiff
(cd "$root" && go build -buildvcs=false -o "$bin" ./cmd/rexdiff) || fail "go build failed"

step "write the fake tools"
mkdir -p "$work/tools" "$work/home"
cat > "$work/tools/pbcopy" << EOF
#!/bin/sh
cat > "$work/clip"
EOF
cp "$work/tools/pbcopy" "$work/tools/wl-copy"
cat > "$work/tools/code" << EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$work/edited"
EOF
cat > "$work/tools/opener" << EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$work/opened"
EOF
cat > "$work/tools/gh" << 'EOF'
#!/bin/sh
cat << 'JSON'
{"number": 7, "title": "Add b", "state": "OPEN", "isDraft": false,
 "url": "https://github.com/o/r/pull/7", "body": "The body of the PR.",
 "author": {"login": "ann"}, "headRefName": "feat", "baseRefName": "main",
 "comments": [{"author": {"login": "bob"}, "body": "Looks good.\n\n<details><summary>The logs</summary>\n\nA hidden line.\n\n</details>",
   "createdAt": "2020-01-01T00:00:00Z"}],
 "statusCheckRollup": [{"name": "test", "conclusion": "SUCCESS"}, {"name": "lint", "conclusion": "FAILURE"}]}
JSON
EOF
chmod +x "$work/tools"/*
tools_path=$work/tools:$(dirname "$(command -v git)"):/usr/bin:/bin

step "create the repository"
repo=$work/repo
mkdir -p "$repo"
cd "$repo"
git init -q -b main
printf 'package a\n\nfunc A() int {\n\treturn 1\n}\n' > a.go
numbered line 1 40 > long.txt
printf '# Title\n\nSome text.\n\n- one\n- two\n' > README.md
{ echo short; printf '%300s' '' | tr ' ' w; echo " needle end"; } > wide.txt
echo bye > gone.txt
git add . && git commit -qm first
git checkout -qb feat
printf 'package a\n\nvar B = 2\n' > b.go
git add . && git commit -qm "add b"
printf 'package a\n\nfunc A() int {\n\tx := 2\n\treturn x\n}\n' > a.go
{ echo first; numbered line 2 39; echo last; } > long.txt
printf '# Title\n\nSome **new** text with [a link](https://example.com/x).\n\n- one\n- two\n' > README.md
{ echo short; printf '%300s' '' | tr ' ' w; echo " needle changed"; } > wide.txt
git rm -q gone.txt
git remote add origin https://github.com/o/r.git
cd "$root"

# The fake agent pi saves its input to agent.in. A line WORK or IDLE in the
# FIFO agent.ctl sets its state.
mkfifo "$work/agent.ctl"
cat > "$work/agent" << EOF
#!/bin/sh
osc() { printf '\033]7501;state=%s:app=pi\007' "\$1"; }
osc idle
echo "agent ready"
while :; do
	read -r cmd < "$work/agent.ctl"
	case \$cmd in
	WORK) osc working; echo "agent working" ;;
	IDLE) osc idle; echo "agent idle" ;;
	esac
done &
stty -icanon -echo min 1 time 0
exec cat > "$work/agent.in"
EOF
chmod +x "$work/agent"

step "start the session $session with the agent"
rex new "$session" --cwd "$repo" -- "$work/agent" > /dev/null
session_id=$(rex session inspect "$session" --json | jq -r .session_id)
agent=$(rex api call -s "$session" session.list_blocks '{}' | jq -r '.blocks[0].block_id')
rex block rename "$agent" agent -s "$session"
agent_shows() { screen=$(rex capture -s "$session" -b "$agent"); case $screen in *"$1"*) ;; *) return 1 ;; esac; }
poll "the agent does not start" agent_shows "agent ready"

shape() { jq -c '[paths | map(if type == "number" then 0 else . end)] | unique'; }

# same_shape NAME fails if the JSON on stdin lacks a key of
# test/testdata/rex/NAME, which the fake rex of the Go tests prints.
same_shape() {
	got=$(shape)
	want=$(shape < "$root/test/testdata/rex/$1")
	missing=$(jq -nc --argjson got "$got" --argjson want "$want" '$want - $got')
	[ "$missing" = "[]" ] || fail "rex output lacks the keys $missing of $1"
}

step "rex prints JSON in the shapes that the fake rex records"
rex api call -s "$session" session.list_blocks '{}' | jq '.blocks[0]' | same_shape block.json
rex session inspect "$session" --json | same_shape session.json
rex block inspect agent -s "$session" --json | same_shape inspect.json
rex block call -s "$session" -b "$agent" process | same_shape process.json
has_status() { rex block call -s "$session" -b "$agent" program_status | jq -e '.records[0]' > /dev/null; }
poll "the agent reports no status" has_status
rex block call -s "$session" -b "$agent" program_status | same_shape status.json
"$REXDIFF_REX" --autostart=false events -s "$session" --json > "$work/events" 2> /dev/null &
events_pid=$!
# Rex sends no event before the stream starts. So report states until one
# arrives.
status_event() {
	echo WORK > "$work/agent.ctl"
	grep -q program_status_changed "$work/events"
}
poll "rex events shows no status change" status_event
kill "$events_pid"
events_pid=
grep -m 1 program_status_changed "$work/events" | same_shape event.json
echo IDLE > "$work/agent.ctl"

block=""
cols=0
rows=0
config='{"focus_agent_after_send": false, "editor": "code -g {file}:{line}", "url_opener": "opener"}'

# start DIR FLAGS...
start() {
	dir=$1
	shift
	block=$(rex run -s "$session" --cwd "$dir" --label under-test --shell=none --keep-open --focus=false --json -- \
		/usr/bin/env PATH="$tools_path" HOME="$work/home" XDG_CONFIG_HOME= REX_CONFIG= WAYLAND_DISPLAY=w \
		EDITOR= VISUAL= REXDIFF_AGENT= REXDIFF_LOG= REXDIFF_RESUME= REXDIFF_LATEST_URL=http://127.0.0.1:9/none \
		GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_ATTR_NOSYSTEM=1 \
		GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=maintenance.auto GIT_CONFIG_VALUE_0=false \
		GIT_CONFIG_KEY_1=gc.auto GIT_CONFIG_VALUE_1=0 \
		"$bin" -log "$work/log" "$@" "$dir" | jq -r '.block_ids[0]')
	# A detached block shows in no app, so no app sets its size.
	rex api call -s "$session" session.detach_block \
		"{\"session_id\": \"$session_id\", \"block_id\": \"$block\"}" > /dev/null
	poll "rexdiff does not start" running
}

running() { [ "$(rex block call -s "$session" -b "$block" process | jq -r '.child.name // ""')" = rexdiff ]; }

# resize COLS ROWS
resize() {
	cols=$1 rows=$2
	rex block call -s "$session" -b "$block" resize "{\"columns\": $cols, \"rows\": $rows}" > /dev/null
	size=$(rex block call -s "$session" -b "$block" size | jq -r '"\(.columns)x\(.rows)"')
	[ "$size" = "${cols}x$rows" ] || fail "the block is $size, want ${cols}x$rows"
}

capture() { screen=$(rex capture -s "$session" -b "$block" | tail -n "$rows"); }

# whole reports whether $screen is a whole frame, and says why not in $why.
whole() {
	why="rexdiff does not run"
	running || return 1
	why="the screen shows a crash"
	if printf '%s\n' "$screen" | grep -Eq 'panic:|goroutine [0-9]+ \[|runtime error|fatal error:|DATA RACE'; then return 1; fi
	why="the terminal wrapped a line"
	[ "$(rex capture -s "$session" -b "$block" | wc -l)" -eq "$(rex capture -s "$session" -b "$block" --unwrap | wc -l)" ] || return 1
	why="the frame is not whole at ${cols}x$rows"
	[ "$cols" -ge 60 ] && [ "$rows" -ge 5 ] || return 0
	case $(printf '%s\n' "$screen" | head -n 1) in
	*"╭"*) on_line "╰" "╯" ;; # a box in place of the screen
	*"1 Changes"*) [ "$(printf '%s\n' "$screen" | wc -l)" -eq "$rows" ] &&
		[ -n "$(printf '%s\n' "$screen" | tail -n 1 | tr -d ' ')" ] ;;
	*) return 1 ;;
	esac
}

shows() {
	for want; do
		case $screen in *"$want"*) ;; *) return 1 ;; esac
	done
}

lacks() {
	for want; do
		case $screen in *"$want"*) return 1 ;; esac
	done
}

on_line() {
	printf '%s\n' "$screen" | while IFS= read -r line; do
		ok=1
		for want; do case $line in *"$want"*) ;; *) ok=0 ;; esac; done
		[ "$ok" = 0 ] || exit 3
	done
	[ $? -eq 3 ]
}

# header NAME checks the file header on the second line.
header() {
	line=$(printf '%s\n' "$screen" | sed -n 2p | sed 's/^ *//')
	case $line in "$1  "*) ;; *) return 1 ;; esac
}

until_screen() {
	end=$(($(date +%s) + wait_seconds))
	while :; do
		capture
		if "$@"; then
			whole && return 0
			[ "$(date +%s)" -lt "$end" ] || fail "$why"
		elif [ "$(date +%s)" -ge "$end" ]; then
			running || fail "rexdiff stops, and the screen does not pass: $*"
			fail "the screen does not pass: $*"
		fi
		sleep 0.05
	done
}

wait_for() { until_screen shows "$@"; }
wait_gone() { until_screen lacks "$@"; }
wait_line() { until_screen on_line "$@"; }
wait_cursor() { until_screen on_line "▶" "$@"; }
wait_header() { until_screen header "$1"; }
wait_frame() { until_screen true; }

# keys takes key names, such as enter, escape or ctrl+f.
keys() { rex send-key -s "$session" -b "$block" "$@"; }

text() { rex send -s "$session" -b "$block" "$1"; }

# at TEXT prints the 1-based column and line of TEXT. The fixture has no
# wide characters, so a character is a column.
at() {
	printf '%s\n' "$screen" | jq -Rnr --arg t "$1" \
		'[inputs] | to_entries | map(select(.value | contains($t))) | first
		| "\((.value | split($t) | first | explode | length) + 1) \(.key + 1)"'
}

# mouse BUTTON COLUMN LINE M|m sends an SGR mouse event. M is a press and
# m is a release.
mouse() { printf '\033[<%d;%d;%d%s' "$@" | rex send -s "$session" -b "$block"; }

click() {
	wait_for "$1"
	set -- $(at "$1")
	mouse 0 "$1" "$2" M
	mouse 0 "$1" "$2" m
}

drag() {
	wait_for "$1" "$2"
	set -- $(at "$1") $(at "$2")
	mouse 0 "$1" "$2" M
	mouse 32 "$3" "$4" M
	mouse 0 "$3" "$4" m
}

file_is() { [ -f "$1" ] && [ "$(cat "$1")" = "$2" ]; }

file_has() {
	f=$1
	shift
	[ -f "$f" ] || return 1
	for want; do grep -qF -- "$want" "$f" || return 1; done
}

column() { at "$1" | cut -d ' ' -f 1; }

line_of() { at "$1" | cut -d ' ' -f 2; }

# A step number N names section N of .doc/user.md.

step "3. rexdiff opens with the changes and finds the agent"
start "$repo" -config "$config"
resize 150 30
wait_for "Changes (5)" "README.md  +1 -1" "send → 1 agent"
labelled() { rex api call -s "$session" session.list_blocks '{}' | jq -e --arg b "$block" '.blocks[] | select(.block_id == $b and .label == "rexdiff")' > /dev/null; }
poll "rexdiff does not label its block" labelled

step "3. j and k move one line"
text j
wait_cursor "# Title"
text jj
wait_cursor "Some text."
text k
wait_cursor "2  2"

step "3. ] and [ go to the next and previous change"
text "]"
wait_cursor "Some text."
# At the last change of a file, ] goes to the next file on a second press.
text "]]"
wait_header a.go
wait_cursor "return 1"
text "[["
wait_header README.md

step "3. f and F go to the next and previous file"
for name in a.go gone.txt long.txt wide.txt; do
	text f
	wait_header "$name"
done
text F
wait_header long.txt

step "3. the right arrow key opens a fold, the left one closes it"
text jjjjjj
wait_for "→ expand fold" "⋯ 32 unmodified lines"
keys right
wait_for "line 20" "← collapse fold"
keys left
wait_for "⋯ 32 unmodified lines"
wait_gone "line 20"

step "3. : goes to a line number"
text ":20"
keys enter
wait_cursor "line 20"
text ':$'
keys enter
wait_cursor last
text ":0"
keys enter
wait_cursor first

step "3. ctrl+f finds text in the file"
keys ctrl+f
text "line 3"
wait_for "find line 3"
keys enter
wait_cursor "line 3"
keys enter
wait_cursor "line 30"
keys escape
wait_gone "find line 3"

step "3. ctrl+f scrolls to a hit far to the right of a cut line"
text f
wait_header wide.txt
text w
wait_gone needle
keys ctrl+f
text needle
keys enter
wait_for "needle changed"
keys escape
wait_gone "find needle"
text w
wait_for "needle end" "needle changed"

step "3. m shows Markdown rendered, with comments"
text FFFF
wait_header README.md
text m
wait_for "• one" "Some new text with a link"
wait_gone "# Title" "**new**"
text ":3"
keys enter
wait_cursor "Some new text"
text c
text "on the markdown"
keys enter
wait_for "💬 on the markdown" "comment added"
text m
wait_for "**new**" "💬 on the markdown"

step "8. y copies the review"
text y
wait_for "copied 1 comment"
wait_gone "💬"
poll "the clipboard has no review" file_has "$work/clip" "README.md:3" "on the markdown"

step "3. ] goes to the next change in rendered Markdown"
text m
wait_for "• one"
text "]"
wait_cursor "Some new text"
text m
wait_for "# Title"

step "3. a drag over the code copies it"
drag Some text.
wait_for copied
poll "the clipboard does not hold \"Some t\"" file_is "$work/clip" "Some t"

step "3. a click on a link opens it"
text m
click "https://example.com/x"
poll "the link does not open" file_is "$work/opened" "https://example.com/x"
text m
wait_for "# Title"

step "5. | shows both sides, if the block is wide enough"
text f
wait_header a.go
text "|"
wait_for "too narrow for split"
resize 220 30
text "|"
wait_line "return 1" "x := 2"
text "|"
until_screen eval '! on_line "return 1" "x := 2"'
resize 150 30
wait_frame

step "5. z hides and shows the file list"
text z
wait_gone "Changes (5)"
text z
wait_for "Changes (5)"

step "p moves the file list: bottom, left, top, right"
list_at() {
	shows "Changes (5)" || return 1
	x=$(column "Changes (5)") y=$(line_of "Changes (5)")
	case $1 in
	bottom) [ "$y" -gt $((rows / 2)) ] ;;
	left) [ "$y" -eq 2 ] && [ "$x" -le 3 ] && on_line "Changes (5)" "│" ;;
	top) [ "$y" -eq 2 ] && [ "$x" -le 3 ] && ! on_line "Changes (5)" "│" ;;
	right) [ "$y" -eq 2 ] && [ "$x" -gt $((cols / 2)) ] ;;
	esac
}
for place in bottom left top right; do
	text p
	until_screen list_at "$place"
done

step "< and > change the width of the file list"
x0=$(column "Changes (5)")
text "<"
until_screen eval '[ "$(column "Changes (5)")" -lt "$x0" ]'
text ">>"
until_screen eval '[ "$(column "Changes (5)")" -gt "$x0" ]'

step "tab moves the focus to the file list and back"
keys tab
text j
wait_header gone.txt
keys tab
text j
wait_cursor bye

step "9. 2 shows all the files, 11. 3 the pull request, 1 the changes"
text 2
wait_for "Files (5)" "b.go"
text 3
wait_for "Add b #7" "checks: ✓ 1  ✗ 1" "Looks good." "▸ The logs"
keys right
wait_for "▾ The logs" "A hidden line."
keys left
wait_gone "A hidden line."

step "11. o opens the pull request in the browser"
text o
wait_for "opened PR in browser"
poll "the pull request does not open" file_has "$work/opened" "https://github.com/o/r/pull/7"
text 1
wait_for "Changes (5)"

step "4. u, b, t and g pick what to compare"
text b
wait_for "vs main" "Changes (6)" "A b.go"
text u
wait_for "Changes (5)"
text t
wait_for "waiting for the first turn"
text g
wait_for "commits · 1 over main" "add b"
keys enter
wait_for "Changes (1)" "A b.go" "var B = 2"

step "4. B picks the base branch, G the commits"
text B
wait_for "base branch for the branch scope" "feat"
wait_line "▶ main" "default current"
text feat
keys enter
wait_for "vs feat" "Changes (5)"
text B
text main
keys enter
wait_for "vs main" "Changes (6)"
text G
wait_for "commits · 1 over main"
keys escape
wait_gone "commits · 1 over main"
text u
wait_for "Changes (5)"

step "10. / finds a file, then tab finds code"
text /long
wait_for "files 1" "▶ long.txt" "preview · long.txt"
keys enter
wait_header long.txt
text /
keys tab
text "line 7"
wait_for "code 1" "7: line 7"
keys enter
wait_cursor "line 7"

step "6. c comments on a line, v on a range"
text FF
wait_header a.go
text "]"
wait_cursor "return 1"
text c
text "first note"
keys enter
wait_for "💬 first note" "●1"
text jvjc
text "range note"
keys enter
wait_for "💬 range note" "●2"

step "7. l lists the comments, e edits one"
text l
wait_for "Comments (2)" "a.go:4 (removed)  first note" "a.go:4-5  range note"
text e
text " more"
keys enter
wait_for "a.go:4 (removed)  first note more"
keys escape
wait_gone "Comments ("

step "n and N go to the next and previous comment"
text ":0"
keys enter
wait_cursor "package a"
text n
wait_cursor "return 1"
text n
wait_cursor "x := 2"
text N
wait_cursor "return 1"

step "7. d deletes a comment"
text ":0"
keys enter
wait_cursor "package a"
text c
text doomed
keys enter
wait_for "💬 doomed" "●3"
text d
wait_gone "💬 doomed"
wait_for "●2"

step "8. s sends the review to the agent"
text s
wait_for "sent 2 comments to agent"
wait_gone "💬"
poll "the agent did not get the review" file_has "$work/agent.in" \
	"a.go:4 (removed)" "return 1" "first note more" "a.go:4-5" "range note"

step "e opens the editor at the cursor line"
text ":0"
keys enter
text "]e"
wait_for "opened a.go"
poll "the editor does not open a.go:4" file_is "$work/edited" "-g $repo/a.go:4"

step "4. t shows the last turn of the agent"
text t
wait_for "waiting for the first turn"
echo WORK > "$work/agent.ctl"
poll "rexdiff does not start the turn" file_has "$work/log" 'msg="turn start"'
echo "package a" > "$repo/turn.go"
echo IDLE > "$work/agent.ctl"
wait_for "Changes (1)" "turn.go"
text u
wait_for "Changes (6)"

step "rexdiff reloads when a file changes"
mkdir "$repo/new"
echo "package c" > "$repo/new/c.go"
wait_for "Changes (7)" "c.go"
rm -r "$repo/new" "$repo/turn.go"
wait_for "Changes (5)"

step "r reloads"
echo fresh > "$repo/fresh.txt"
text r
wait_for "Changes (6)" "fresh.txt"
rm "$repo/fresh.txt"
wait_for "Changes (5)"

step "12. ? shows all the keys"
text "?"
wait_for "search files and code" "pick commits" "move list"
keys escape
wait_gone "pick commits"

step "s sends a review of more than 64 KB in parts"
before=$(wc -c < "$work/agent.in")
numbered "<&> a long review line" 1 3000 > "$work/long"
echo "END OF THE LONG REVIEW" >> "$work/long"
text "]c"
printf '\033[200~' | rex send -s "$session" -b "$block"
size=$(wc -c < "$work/long")
i=0
while [ $((i * 30000)) -lt "$size" ]; do
	dd if="$work/long" bs=30000 skip=$i count=1 2> /dev/null | rex send -s "$session" -b "$block"
	i=$((i + 1))
done
printf '\033[201~' | rex send -s "$session" -b "$block"
keys enter
wait_for "comment added"
text s
wait_for "sent 1 comment to agent"
long_arrived() { [ $(($(wc -c < "$work/agent.in") - before)) -gt "$size" ] && file_has "$work/agent.in" "END OF THE LONG REVIEW"; }
poll "the agent did not get all of the long review" long_arrived

step "the screen stays whole from 1x1 to 300x80"
for size in 10x3 1x1 20x5 40x10 300x80 13x7 150x30; do
	resize "${size%x*}" "${size#*x}"
	wait_frame
done
wait_for "Changes (5)"

# with_box OPEN SHOWS CLOSE checks that the box from OPEN still shows
# SHOWS after many resizes. CLOSE closes it.
with_box() {
	eval "$1"
	wait_for "$2"
	for size in 10x3 300x80 1x1 20x5 200x50 40x10 150x30; do
		resize "${size%x*}" "${size#*x}"
		wait_frame
	done
	wait_for "$2"
	eval "$3"
	wait_gone "$2"
}

step "the help stays open while the block resizes"
with_box 'text "?"' "pick commits" 'keys escape'

step "search stays open while the block resizes"
with_box 'text /long' "preview · long.txt" 'keys escape'

step "the comment box stays open while the block resizes"
with_box 'text c; text "typed so far"' "typed so far" 'keys escape'

step "the find bar stays open while the block resizes"
with_box 'keys ctrl+f; text Title' "find Title" 'keys escape'

step "the quit prompt stays open while the block resizes"
text c
text unsent
keys enter
wait_for "💬 unsent"
with_box 'text q' "1 unsent comment will be lost" 'keys escape'
wait_for "💬 unsent"

step "q asks before it drops unsent comments, Q quits"
text q
wait_for "1 unsent comment will be lost"
text Q
stopped() { ! running; }
poll "Q does not quit" stopped

step "create a repository of hostile names and contents"
hostile=$work/hostile
mkdir -p "$hostile"
cd "$hostile"
git init -q -b main
long_dir=""
for i in 01 02 03 04 05 06 07 08 09 10; do long_dir=${long_dir}very_long_directory_name_$i/; done
mkdir -p "$long_dir"
echo "package main" > "${long_dir}xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx_final_file_name.go"
printf 'title\n' > "$(printf 'esc\033]0;PWNED\007title.txt')"
printf 'ctl\n' > "$(printf 'ctl\001\007\033[31mRED\033[0m.txt')"
printf 'nl\n' > "$(printf 'new\nline.txt')"
printf 'rtl\n' > "$(printf '\342\200\256evil_rtl_override_gnp.exe')"
printf 'zw\n' > "$(printf 'zero\342\200\213width\357\273\277bom.txt')"
printf 'cjk\n' > 中文中文中文中文中文中文中文中文中文中文.txt
printf 'emoji\n' > 😀😀😀😀😀😀😀😀😀😀😀😀.txt
printf 'normal\n\033[31mred\033[0m\n\033[2J\033[Hclear\n\033]0;PWNED\007title\n\007bell\010back\177del\n' > ansi.txt
git add . && git commit -qm "hostile $(printf '\033]0;PWNED\007') message"
find . -path ./.git -prune -o -type f -exec sh -c \
	'for f; do printf "added \033[35mmagenta\033[0m \033]0;PWNED\007 中😀 line\n" >> "$f"; done' _ {} +
printf 'new\n' > "$(printf 'untracked \033[7mrev\033]0;PWNED\007.txt')"
cd "$root"

step "hostile names show escaped and do not change the terminal"
start "$hostile" -config "$config"
resize 120 30
wait_for "Changes (10)" '"new\nline.txt"' 'WNED\atitle.txt"' "␛]0;PWNED␇ 中😀 line"
title_ok() { ! rex block call -s "$session" -b "$block" title | grep -q PWN; }
for i in 1 2 3 4 5 6 7 8 9 10; do
	text f
	wait_frame
done
for size in 40x10 10x3 300x80 120x30; do
	resize "${size%x*}" "${size#*x}"
	wait_frame
done
title_ok || fail "a file changed the title of the block"
text q
poll "q does not quit" stopped

echo "PASS: $n steps"
