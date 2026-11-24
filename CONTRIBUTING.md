# Contributing to Leasorix

Thanks for your interest! Leasorix is in **beta** – the most valuable
contribution right now is **testing in your (lab) network** and reporting
what does not work.

## Reporting bugs and ideas

- Bugs and feature requests: [open an issue](https://github.com/meister119/leasorix/issues/new/choose).
- Security problems: **privately**, see [SECURITY.md](SECURITY.md).
- When you migrate from Windows DHCP or ISC dhcpd, tell us what was missing
  or imported incorrectly – that helps a lot.

## Pull requests

1. Open an issue first for larger changes, so we can agree on the approach.
2. Keep changes focused; add or update tests for what you change.
3. Run the checks locally before pushing:

   ```sh
   make test      # Go unit tests (race detector)
   make ui-test   # web UI type check + unit tests
   make lint      # golangci-lint v2
   ```

   The network tests (`make test-integration`, `make test-ha`,
   `make test-dns`) need root and run in CI as well.
4. The REST API is generated from `internal/api/openapi.yaml`: change the
   spec and run `make generate`, do not edit `internal/api/gen` by hand.
5. Update the documentation in `docs/` when behaviour or configuration
   changes.

## License

Leasorix is licensed under the [GNU AGPL v3](LICENSE). By submitting a
contribution you agree that it is licensed under the same terms.
