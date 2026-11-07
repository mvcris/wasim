# Changelog

Notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Console login. `wasim user add`, `passwd`, `remove` and `list` manage users in
  the database; once one exists, the console asks for a login and its API
  accepts HTTP Basic credentials from scripts. Without users the console stays
  open, as before.

## [0.1.0] - 2026-11-05

### Added

- Fake Graph API: text, interactive button and list, and template sends, mark as
  read, media lookup and download, and the template catalog with cursor paging.
- Signed webhooks with a persistent outbox, retries, concurrent delivery, the
  verification handshake, duplicates and bad signature scenarios.
- 24 hour customer service window enforcement.
- Failure injection per error code, through either the HTTP or the webhook channel.
- Browser console that plays the WhatsApp user.
- SQLite persistence and seed files for templates and contacts.
- Compatibility guide listing what is emulated, what is not, and known drifts
  from the real platform.
- Browser end-to-end tests of the console with Playwright, and release
  workflows for binaries and a multi-architecture container image.

### Fixed

- A webhook resent with a bad signature now carries a wrong signature even when
  no app secret is configured, instead of going out unsigned.
- Webhook delivery no longer follows redirects: only HTTP 200 acknowledges.
- The console computes the 24 hour window badge and back-dated messages with
  the simulator's clock.
- The failure rule form labels its fields explicitly and resets after a rule is
  added.

[Unreleased]: https://github.com/mvcris/wasim/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mvcris/wasim/releases/tag/v0.1.0
