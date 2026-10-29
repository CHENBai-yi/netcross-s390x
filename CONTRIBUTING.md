# Contributing to NetCross-s390x

Thank you for your interest in improving multi-architecture support for Linux on IBM Z and LinuxONE!

## Development Workflow

1. **Fork and Clone**:
   ```bash
   git clone https://github.com/CHENBai-yi/netcross-s390x.git
   cd netcross-s390x
   ```

2. **Run Tests**:
   ```bash
   go test -v ./...
   ```

3. **Cross-Compilation for s390x**:
   Ensure your changes do not introduce Big-Endian byte order regressions:
   ```bash
   GOOS=linux GOARCH=s390x go build -v ./...
   ```

4. **Pull Requests**:
   - Ensure all tests pass.
   - Follow Conventional Commits for commit messages (`feat:`, `fix:`, `docs:`, `test:`).
