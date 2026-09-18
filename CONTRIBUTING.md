# Contributing to Envoryx

Thanks for considering a contribution. Bug reports, feature ideas, translations
and code are all welcome.

## Before you start

- **Bugs and ideas:** open an issue. For anything larger than a small fix,
  please discuss it in an issue first so we agree on the approach before you
  spend time on it.
- **Security issues:** do not open a public issue – see
  [SECURITY.md](SECURITY.md).
- **Setup, tests and coding conventions:** see
  [DEVELOPMENT.md](DEVELOPMENT.md). `make test` must pass before you open a
  pull request.

## Contributor License Agreement

Every contributor must sign the
[Envoryx Contributor License Agreement](CLA.md) once before their first pull
request can be merged. A bot on the pull request will guide you through it;
signing takes a minute and is done through your GitHub account. The text you
sign is published as a
[Gist](https://gist.github.com/seramos/a10b5f9cc4ac13ce72e06b6b090dc83f) and
is identical to `CLA.md` in this repository.

In short: you keep your copyright and grant Envoryx a license to use your
contribution – including under licenses other than the AGPL, so that the
project can be offered under additional terms or handed over to a successor
without asking every past contributor. In return, Envoryx commits that your
contribution always stays available as open source.

If you contribute on behalf of your employer, your employer signs the
Corporate version of the agreement (see [CLA.md](CLA.md)) and lists you as
an authorised contributor. Send the completed signature table to the
maintainer at the address in the repository's commit history or via a GitHub
issue asking for a private channel.

## Pull requests

- One topic per pull request; keep unrelated refactoring separate.
- Match the surrounding code: Go is `gofmt`-clean and passes `go vet`
  (`make lint`); the frontend passes `tsc` (`make test-web`).
- Add or update tests for behaviour changes. Backend tests use the fake Docker
  engine; see DEVELOPMENT.md for the patterns.
- User-facing strings go through i18n (`web/src/i18n/`), English and German.
- Write the commit message in the imperative ("Add …", "Fix …") and explain
  *why* in the body when the diff does not make it obvious.
- Mark code you did not write yourself (copied snippets, vendored files)
  clearly with its source and license in the pull request.

## Licence of contributions

Envoryx is licensed under the GNU Affero General Public License v3.0. By
submitting a contribution you agree that it is published under that license
and under the terms of the CLA above.
