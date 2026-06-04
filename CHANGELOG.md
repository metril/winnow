# Changelog

## [1.0.1](https://github.com/metril/winnow/compare/v1.0.0...v1.0.1) (2026-06-04)


### Bug Fixes

* pin capture to performance cores so rtlamr keeps up ([0e9d0a9](https://github.com/metril/winnow/commit/0e9d0a9c865d6f0ce00ffcec1cc040b2439c5b70))
* serialize and back off USB resets so a marginal dongle isn't driven off the bus ([11e6c2f](https://github.com/metril/winnow/commit/11e6c2fb9a0e3ec5974fe284ad7676c607d78960))
* wait for rtl_tcp to listen before starting rtlamr so capture isn't killed mid-open ([b7fb53a](https://github.com/metril/winnow/commit/b7fb53a1f7f492c752a39cd3d181b644c186f6fe))

## 1.0.0 (2026-06-02)


### Bug Fixes

* track worker/Dockerfile so CI can build the worker image ([a1ce3eb](https://github.com/metril/winnow/commit/a1ce3eb871ec05d47356fe702011e05e9bf313eb))


### Continuous Integration

* build & publish multi-arch images to GHCR with release-please ([b943d67](https://github.com/metril/winnow/commit/b943d67b6a3963ce89db2cfe4de968fb9232a815))
