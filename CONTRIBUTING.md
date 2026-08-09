# Contributing

1. Fork the repository and create a focused branch.
2. Run `make test` and `go vet ./...` before opening a pull request.
3. Keep device-specific protocol behavior inside a device adapter.
4. Add a fake-device test for new commands and capability changes.
5. Never commit bot tokens, TV tokens, certificates, database files, or real network captures.

Hardware integration tests may be run manually with a real Samsung TV, but the default test suite must not require LAN hardware.
