# Contributing

Pull requests are welcome.

Use Go 1.20 or later. The code is kept to Go 1.20, the last release that runs on Windows 7, so please do not use newer language features or standard-library packages. Run the demos from the repository folder. Before sending a change, run:

```sh
go vet ./...
go test ./...
```

Please add a test for behavior that your change affects. Use made-up data in tests and examples. Do not add real restaurant data or printer addresses.

Read [CLA.md](CLA.md). The comment `I have read the CLA and I agree to it.` is required on your pull request before merge.
