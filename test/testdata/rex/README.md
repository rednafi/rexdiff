These files are the output of a real rex (0.0.1792787366-g623b2d). The
fake rex of the tests prints them. Each {{NAME}} is a value that a test
fills in: a block ID, a label, a path, an app, a state, or a time.

- block.json: one block of `rex api call -s S session.list_blocks {}`
- session.json: `rex session inspect S --json`, without the layout
- process.json: `rex block call -s S -b ID process`
- status.json: `rex block call -s S -b ID program_status`
- event.json: a line of `rex events -s S --json` when the status changes
- inspect.json: `rex block inspect LABEL -s S --json`, without the API

To record them again, run a shell in a new Rex session, report a status
with `printf '\033]7501;state=working:app=pi\007'`, and run the commands.
