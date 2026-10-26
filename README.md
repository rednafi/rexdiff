<div align="center">
  <img src=".asset/logo.svg" alt="rexdiff logo" width="160">
  <h1>rexdiff</h1>
  <p>
    <b>Review what your coding agent changed, without leaving <a href="https://www.superlogical.com/rex">Rex</a>.</b><br>
    Think <a href="https://github.com/persiyanov/herdr-reviewr">herdr-reviewr</a>, but for Rex.
  </p>
  <p>
    <a href="https://github.com/rednafi/rexdiff/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/rednafi/rexdiff?style=flat-square&color=e8409a"></a>
    <a href="https://github.com/rednafi/rexdiff/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/rednafi/rexdiff/ci.yml?branch=master&style=flat-square&label=ci"></a>
    <img alt="macOS" src="https://img.shields.io/badge/macOS-arm64%20%7C%20amd64-8b4cf7?style=flat-square">
    <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-ff5a3c?style=flat-square"></a>
  </p>
  <p>
    <b>
      <a href=".doc/user.md">User guide</a>
      &nbsp;·&nbsp;
      <a href=".doc/arch.md">Architecture</a>
      &nbsp;·&nbsp;
      <a href=".doc/dev.md">Development</a>
      &nbsp;·&nbsp;
      <a href=".doc/test.md">Tests</a>
    </b>
  </p>
</div>

<p align="center">
  <a href="https://github.com/user-attachments/assets/d56af0f9-e1ff-422d-b0e7-0e5afe3320ae">
    <img src=".asset/hero.png" alt="rexdiff beside a coding agent, with two review comments in the diff">
  </a>
  <br>
  <sub><a href="https://github.com/user-attachments/assets/d56af0f9-e1ff-422d-b0e7-0e5afe3320ae">Watch the demo</a></sub>
</p>

## Why

- The diff opens in a Rex pane beside the agent, so you don't switch apps.
- The last-turn scope shows only what the agent changed in its most recent turn.
- Press `s` and rexdiff pastes your comments into the agent's input, each with its file
  and line.

## Install

```sh
brew tap rednafi/rexdiff https://github.com/rednafi/rexdiff
brew trust rednafi/rexdiff
brew install --cask rexdiff
rexdiff install
```

Then press <kbd>Shift</kbd>+<kbd>Cmd</kbd>+<kbd>R</kbd> next to an agent.
