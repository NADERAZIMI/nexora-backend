# Dependency and License Policy

**Status:** Approved for B01
**Owner:** Security Engineering

All third-party code must be pinned, reviewed, and license-compatible with a
proprietary exchange backend.

---

## 1. Allowed licenses

- MIT
- Apache-2.0 and Apache-2.0 WITH LLVM-exception
- BSD-2-Clause and BSD-3-Clause
- ISC
- Unicode-3.0
- Zlib
- MPL-2.0 (file-level copyleft; requires review)
- CC0-1.0
- OpenSSL (requires review)

Copyleft licenses such as GPL, AGPL, LGPL, SSPL, and Commons Clause are not
allowed without written legal approval. Network-copyleft licenses are treated as
blocking.

## 2. Enforcement

| Ecosystem | Lockfile | Scan |
|-----------|----------|------|
| Rust | `Cargo.lock` | `cargo-deny` advisories, bans, licenses, sources |
| Go | `go.sum` | `govulncheck`, `go mod verify`, CI dependency review |
| Node | `package-lock.json` | `npm audit --audit-level=critical` |
| Containers | images in `compose.yaml` | Trivy filesystem/image scan, SBOM |

## 3. Rules

1. Do not add a dependency for functionality that is small and security-
   critical; implement and test it locally.
2. Pin exact versions in production manifests. Lockfiles are mandatory.
3. New dependencies require review for maintenance, license, and transitive
   risk.
4. Unmaintained abstractions and abandoned crates are rejected.
5. Build scripts and post-install scripts are reviewed; CI installs Node
   dependencies with `--ignore-scripts` unless a script is explicitly approved.
6. Security advisories are fixed before release. Exceptions require a written
   risk acceptance with an expiry date.
7. SBOMs are generated for release artifacts and retained.
8. Git dependencies are forbidden unless pinned to a commit and approved.

## 4. Known B01 dev-tooling advisories

`npm audit --audit-level=critical` passes. Non-critical advisories remain in
Markdown and OpenAPI lint tooling (`braces`, `katex`, `smol-toml`). These tools
process repository-controlled files and are not deployed to production. They
must be upgraded when upstream fixes are available; critical advisories block
CI immediately.

## 5. Go license review

Go dependencies are currently Apache-2.0, BSD, or MIT. Automated Go license
reporting is added with the first production service in a later phase. Any new
Go dependency requires license review in the pull request.
