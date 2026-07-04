# Changelog

## [1.12.0](https://github.com/metril/winnow/compare/v1.11.1...v1.12.0) (2026-07-04)


### Features

* **autowindow:** detect sharp load changes automatically, on by default ([#32](https://github.com/metril/winnow/issues/32)) ([48818a8](https://github.com/metril/winnow/commit/48818a8bca55663abefe4e8639778116475303dc))


### Bug Fixes

* **loadtests:** align the start-test form on one baseline ([#30](https://github.com/metril/winnow/issues/30)) ([dacbc92](https://github.com/metril/winnow/commit/dacbc927c42b4c3fcfb65aac90c00b140ff243d4))

## [1.11.1](https://github.com/metril/winnow/compare/v1.11.0...v1.11.1) (2026-07-04)


### Bug Fixes

* **charts:** draw the bill-estimate line in full-contrast ink ([#28](https://github.com/metril/winnow/issues/28)) ([6b4fce8](https://github.com/metril/winnow/commit/6b4fce84a6587d951c57926634e437f1e706b476))

## [1.11.0](https://github.com/metril/winnow/compare/v1.10.0...v1.11.0) (2026-07-04)


### Features

* **usage:** compare meters as aligned panels, quick meter cycling ([#27](https://github.com/metril/winnow/issues/27)) ([f955ace](https://github.com/metril/winnow/commit/f955acef9e8998a4b8925a445f1aae797b38b77b))
* **utility:** project the daily bill estimate past the last posted bill ([#26](https://github.com/metril/winnow/issues/26)) ([f721f1d](https://github.com/metril/winnow/commit/f721f1ddc89cbc38ce0cbb57d01f509f1c9649bf))


### Bug Fixes

* **live:** stop per-packet page re-renders; controlled Utility paging ([#24](https://github.com/metril/winnow/issues/24)) ([9cd0d91](https://github.com/metril/winnow/commit/9cd0d910c3bcbaac5bb14f2ef6b4e4d481f4096b))

## [1.10.0](https://github.com/metril/winnow/compare/v1.9.0...v1.10.0) (2026-07-04)


### Features

* **overview:** center the dashboard on your meter with honest publish status ([#19](https://github.com/metril/winnow/issues/19)) ([e857e02](https://github.com/metril/winnow/commit/e857e02b6ba9782194f2e43c2ce231074a47a7b5))
* **usage:** browse any meter's consumption by day, week, month or year ([#17](https://github.com/metril/winnow/issues/17)) ([8e6943c](https://github.com/metril/winnow/commit/8e6943c97ab1e61cf47876cdb38f9dff40e17725))
* **worker:** backfill reference gaps from HA long-term statistics ([#21](https://github.com/metril/winnow/issues/21)) ([b9205a3](https://github.com/metril/winnow/commit/b9205a399656c07bf91415360dcadfe676e197ca))


### Bug Fixes

* **analytics:** glitch/rollover-aware charts in local time, honest UI states ([#22](https://github.com/metril/winnow/issues/22)) ([bcfa8a8](https://github.com/metril/winnow/commit/bcfa8a83f2622de4d68c05794778bbc1d54f239b))
* **reference:** bound gap-fill carry, gate the daily screen on real coverage ([#20](https://github.com/metril/winnow/issues/20)) ([db146d5](https://github.com/metril/winnow/commit/db146d50e8ad6637c8af40a107a2580bfcc86234))


### Performance Improvements

* **db:** hourly-aggregate reads, publish N+1 fix, gzip, screen memo ([#23](https://github.com/metril/winnow/issues/23)) ([7b1841a](https://github.com/metril/winnow/commit/7b1841a7d4131b0645447d65e86a88137e73188a))

## [1.9.0](https://github.com/metril/winnow/compare/v1.8.0...v1.9.0) (2026-06-10)


### Features

* **identify:** daily physics screen, glitch-filtered deltas, confidence fixes ([#15](https://github.com/metril/winnow/issues/15)) ([8f51a40](https://github.com/metril/winnow/commit/8f51a405a45467d35627d712b02de73cffd6c9b1))

## [1.8.0](https://github.com/metril/winnow/compare/v1.7.0...v1.8.0) (2026-06-07)


### Features

* **utility:** full history, browsable charts, dashboard daily estimate ([#13](https://github.com/metril/winnow/issues/13)) ([dc5d0c7](https://github.com/metril/winnow/commit/dc5d0c74beb96e4c6d562c3fab5e606d446a35bf))

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
