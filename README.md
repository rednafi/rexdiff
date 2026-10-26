<p align="center">
  <img src=".asset/logo.svg" alt="rexdiff logo" width="140">
</p>

<h1 align="center">rexdiff</h1>

A diff viewer for coding agents in [Rex]. Think [herdr-reviewr], but for Rex.

It opens next to the agent and shows its changes. You comment on lines and send
the comments back to the agent.

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
