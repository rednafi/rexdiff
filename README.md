<div align="center">
  <img src=".asset/logo.svg" alt="rexdiff logo" width="160">
  <h1>rexdiff</h1>
  <p>
    A diff viewer for coding agents in <a href="https://www.superlogical.com/rex">Rex</a>.<br>
    Think <a href="https://github.com/persiyanov/herdr-reviewr">herdr-reviewr</a>, but for Rex.
  </p>
  <p>
    It opens next to the agent and shows its changes.<br>
    You comment on lines and send the comments back to the agent.
  </p>
</div>

https://github.com/user-attachments/assets/e44159eb-3b02-4ea8-88ca-f6314261c1aa

## Install

```sh
brew tap rednafi/rexdiff https://github.com/rednafi/rexdiff
brew trust rednafi/rexdiff
brew install --cask rexdiff
rexdiff install
```

Then press Shift+Cmd+R next to an agent.

## Docs

- [User guide](.doc/user.md)
- [Architecture](.doc/arch.md)
- [Development](.doc/dev.md)
- [Tests](.doc/test.md)

<!-- references -->

[Rex]: https://www.superlogical.com/rex
[herdr-reviewr]: https://github.com/persiyanov/herdr-reviewr
