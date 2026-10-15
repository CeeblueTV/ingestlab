# Contributing

Your contributions to this project are highly appreciated. Please adhere to this guide to
streamline the process for everyone.

By contributing code to this project, you agree to license your contribution under the
[GNU Affero General Public License](/LICENSE).

## Issues

Use issues exclusively for bug reporting and proposing enhancements.

When reporting a bug, include the exact command line, `ingestlab --version`, how it was
installed (Docker image or local build, and then which FFmpeg), the server you published to,
and the output of a run with `-v`. A stream impairment tool is easy to blame for what the
server did, so say what you expected to see on the wire and what you saw instead.

🐛 **[File a bug report](https://github.com/CeeblueTV/ingestlab/issues/new?template=01-bug.md)**
✨ **[Propose enhancement](https://github.com/CeeblueTV/ingestlab/issues/new?template=02-enhancement.md)**

## Pull Requests

To contribute, fork the project, then submit a pull request with your changes, targeting the
**main** branch. A pull request needs one approval and a green CI before it is merged.

Pull requests are squash-merged, so the **pull request title** becomes the commit on `main`
and must follow [conventional commits](https://www.conventionalcommits.org/en/v1.0.0/). CI
checks it. The title drives the release that follows the merge:

| Title | Release |
|---|---|
| `fix: ...` | patch |
| `feat: ...` | minor |
| `feat!: ...`, or a `BREAKING CHANGE:` footer | major |
| `docs:`, `test:`, `ci:`, `build:`, `chore:`, `refactor:` | none |

A change to a flag's name or meaning is a breaking change.

## Development

See [Building locally](README.md#building-locally) for the prerequisites. Before pushing:

```sh
gofmt -l .
go vet ./...
go test ./...
```

The read-back tests publish for real and assert on what arrived, which is the only way to
catch an impairment the muxer quietly undid. They skip unless `INGESTLAB_SAMPLE` points at a
source whose first audio stream is AAC. CI runs the short tests inside the Docker image; to
reproduce it exactly:

```sh
docker build --target test -f docker/Dockerfile .
```

A few rules keep runs reproducible and measurements honest. Please keep to them:

- Timestamps stay rational end to end; never round them to milliseconds.
- Durations go through `internal/units`, so every option accepts `10s`, `1500ms`, `2m`.
- Every random draw comes from `--seed`, and schedules run on the media clock.
- With `--epoch` unset, the stream is a true passthrough: no decoder, encoder or filter.

## [Developer's Certificate of Origin 1.1](https://github.com/nodejs/node/blob/main/CONTRIBUTING.md#developers-certificate-of-origin-11)

By making a contribution to this project, I certify that:

* (a) The contribution was created in whole or in part by me and I
  have the right to submit it under the open source license
  indicated in the file; or

* (b) The contribution is based upon previous work that, to the best
  of my knowledge, is covered under an appropriate open source
  license and I have the right under that license to submit that
  work with modifications, whether created in whole or in part
  by me, under the same open source license (unless I am
  permitted to submit under a different license), as indicated
  in the file; or

* (c) The contribution was provided directly to me by some other
  person who certified (a), (b) or (c) and I have not modified
  it.

* (d) I understand and agree that this project and the contribution
  are public and that a record of the contribution (including all
  personal information I submit with it, including my sign-off) is
  maintained indefinitely and may be redistributed consistent with
  this project or the open source license(s) involved.

## Questions?

For inquiries other than bug reports or feature requests, please send an email to our
support team at <support@ceeblue.net>.
