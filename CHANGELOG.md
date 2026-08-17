## v0.1.2 (2026-08-17)

### Bug Fixes

* remove commit_changelog:false with no replacement plugin - CHANGELOG.md was never updated
* resolve OCI digests via jq instead of oras tag lookup (Docker rewrites bare tags to :latest)

### Other Changes

* **deps:** Bump reviewdog/action-actionlint from 1.72.0 to 1.73.0 (#3)
* **deps:** Bump docker/login-action from 4.5.0 to 4.5.1 (#4)
* **deps:** Bump docker/metadata-action from 5.9.0 to 6.2.0 (#5)
* **deps:** Bump actions/checkout from 6.0.3 to 7.0.1 (#6)
* **deps:** Bump actions/setup-go from 6.4.0 to 7.0.0 (#7)

