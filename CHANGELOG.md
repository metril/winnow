# Changelog

## [1.7.0](https://github.com/metril/winnow/compare/v1.6.0...v1.7.0) (2026-06-06)


### Features

* **utility:** dashboard view of billed energy + cost + bill reconciliation ([#11](https://github.com/metril/winnow/issues/11)) ([580334c](https://github.com/metril/winnow/commit/580334c335336ac8b45a45bfa2f1913ebe080048))


### Bug Fixes

* **identify:** align utility daily breakdown to HA's local timezone ([#10](https://github.com/metril/winnow/issues/10)) ([7b3a5a6](https://github.com/metril/winnow/commit/7b3a5a611f1b2bdc39b6fe3100c48539c54df0ba))

## [1.6.0](https://github.com/metril/winnow/compare/v1.5.0...v1.6.0) (2026-06-06)


### Features

* **identify:** use utility-bill (Opower) energy as an independent meter signal ([#9](https://github.com/metril/winnow/issues/9)) ([bf2235c](https://github.com/metril/winnow/commit/bf2235cb1dd823dde47ce1614aa4a4a745618bbe))


### Bug Fixes

* **compose:** give the db container enough /dev/shm for parallel VACUUM/ANALYZE ([fb1e80d](https://github.com/metril/winnow/commit/fb1e80d4b63db5be555bfad981d0523b5cc24b43))

## [1.5.0](https://github.com/metril/winnow/compare/v1.4.0...v1.5.0) (2026-06-05)


### Features

* **identify:** research-backed confidence model and calibration engine ([55b51f9](https://github.com/metril/winnow/commit/55b51f90ff99b216e80085f80f3c92409e3ae46c))
* **identify:** selectable-meter chart, commodity toggle, and legible calibration UX ([854bc12](https://github.com/metril/winnow/commit/854bc12f64ee1599d97fae6e14a12ee301eb8fe1))
* **ui:** interactive chart legends and accessible Toggle/Dialog ([4db8e59](https://github.com/metril/winnow/commit/4db8e59fad2aeda59ffc2af26276031c6b5c44b0))


### Bug Fixes

* **ui:** consistent loading/empty states, aria-labels, and token polish ([5acc501](https://github.com/metril/winnow/commit/5acc501acea8f929d37cedb0718e31583b0f47f1))

## [1.4.0](https://github.com/metril/winnow/compare/v1.3.0...v1.4.0) (2026-06-04)


### Features

* energy reconciliation and a configurable comparison bucket on Identify ([2fd3ab0](https://github.com/metril/winnow/commit/2fd3ab044908be7c41e8a5943afcfdfed75b0a96))


### Bug Fixes

* correlate meter consumption as cross-bucket energy, not within-minute max-min ([aa9db02](https://github.com/metril/winnow/commit/aa9db025873cac079adac5657508c66df888884a))

## [1.3.0](https://github.com/metril/winnow/compare/v1.2.0...v1.3.0) (2026-06-04)


### Features

* live remote-agent updates and a confirm step before approval ([013b073](https://github.com/metril/winnow/commit/013b07332ba615c38f52edb158022dd8f8d5a206))


### Bug Fixes

* capture rate no longer ramps from zero after a refresh ([d59bb05](https://github.com/metril/winnow/commit/d59bb05279981ae1b91f05d9c3f7fbfece869eac))
* stop candidate meter chart lines from diving to zero ([82cf18f](https://github.com/metril/winnow/commit/82cf18fcd157e6db7c157ee400f040e9cd5a39a3))

## [1.2.0](https://github.com/metril/winnow/compare/v1.1.0...v1.2.0) (2026-06-04)


### Features

* agent pending-approval and trust-on-first-use server key ([945761e](https://github.com/metril/winnow/commit/945761e2a8d3356069c7655f0eb73a9ef0c0c9f2))
* unify track/publish into confirmable toggles across pages ([d2ec99b](https://github.com/metril/winnow/commit/d2ec99b6e472e5eeb6066dfe16b660dc2ae5666e))


### Bug Fixes

* capture rate survives a dashboard refresh ([1da8f7c](https://github.com/metril/winnow/commit/1da8f7c17aa0e1bf203621e13cd085f9ecdc7ea3))
* chart meter usage as cross-bucket delta so lines don't dive to zero ([12c6cf0](https://github.com/metril/winnow/commit/12c6cf0ee15d44844d9c1ea07a3c0e425632d6f9))
* copy buttons work in non-secure (plain-HTTP) contexts ([873b8fa](https://github.com/metril/winnow/commit/873b8fa9b1b043ac2dbb1e9bb5985253e426d38a))
* make TimescaleDB policy setup idempotent ([14e7073](https://github.com/metril/winnow/commit/14e7073eab5eddd794d930b51f67cdd7c669052b))

## [1.1.0](https://github.com/metril/winnow/compare/v1.0.1...v1.1.0) (2026-06-04)


### Features

* agent re-enumerates and recovers a dropped dongle mid-session ([41d3a72](https://github.com/metril/winnow/commit/41d3a72d97f1f9cb5309138f78c2dcf4d6cbaeb9))


### Bug Fixes

* stop rtl_test gracefully and settle so enumeration doesn't wedge the dongle ([dc610c4](https://github.com/metril/winnow/commit/dc610c4a7c232486634310541d8ea0c78d962ed6))

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
