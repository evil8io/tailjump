# Changelog

## [1.6.0](https://github.com/evil8io/tailjump/compare/v1.5.0...v1.6.0) (2026-09-14)


### Features

* **cli:** improve the command-line experience ([#31](https://github.com/evil8io/tailjump/issues/31)) ([7c5a839](https://github.com/evil8io/tailjump/commit/7c5a8396a1e600a1802abd3ec90e1315775328b1))

## [1.5.0](https://github.com/evil8io/tailjump/compare/v1.4.0...v1.5.0) (2026-09-12)


### Features

* **session:** open one ssh connection per protocol on the ssh transport ([#27](https://github.com/evil8io/tailjump/issues/27)) ([63a552f](https://github.com/evil8io/tailjump/commit/63a552f79717ee4cacc49ebb38c329513660ba45))

## [1.4.0](https://github.com/evil8io/tailjump/compare/v1.3.2...v1.4.0) (2026-09-12)


### Features

* forward icmp echo and traceroute, add a protocol set ([#24](https://github.com/evil8io/tailjump/issues/24)) ([ae56a56](https://github.com/evil8io/tailjump/commit/ae56a562e67e04ee781845d9499b3c75e7fc3b2e))

## [1.3.2](https://github.com/evil8io/tailjump/compare/v1.3.1...v1.3.2) (2026-09-12)


### Bug Fixes

* **session:** warn on a flapping path instead of an endpoint inside the networks ([#22](https://github.com/evil8io/tailjump/issues/22)) ([2629fef](https://github.com/evil8io/tailjump/commit/2629fef93ac0c388c355cbfb95374c8939ec142a))

## [1.3.1](https://github.com/evil8io/tailjump/compare/v1.3.0...v1.3.1) (2026-09-12)


### Bug Fixes

* **session:** keep the session routes out of the main table ([#20](https://github.com/evil8io/tailjump/issues/20)) ([de064a6](https://github.com/evil8io/tailjump/commit/de064a6222c2fc51ee69b273d42fde4b0a9963f0))

## [1.3.0](https://github.com/evil8io/tailjump/compare/v1.2.0...v1.3.0) (2026-09-12)


### Features

* **cli:** add command aliases and rename remote rm to remove ([#15](https://github.com/evil8io/tailjump/issues/15)) ([c091fae](https://github.com/evil8io/tailjump/commit/c091fae2593359470c5691bb124fef639b5a6ab8))
* **cli:** show the session status and the tailscale path in list ([#16](https://github.com/evil8io/tailjump/issues/16)) ([147aa28](https://github.com/evil8io/tailjump/commit/147aa2875945e9b6895361fb759c0b82e568c6ee))
* **transport:** add the QUIC data plane over the tailnet ([#10](https://github.com/evil8io/tailjump/issues/10)) ([0fad289](https://github.com/evil8io/tailjump/commit/0fad28918ec0812d9d8848642778fc3881c25800))

## [1.2.0](https://github.com/evil8io/tailjump/compare/v1.1.0...v1.2.0) (2026-09-11)


### Features

* **cli:** add config management commands and client debug logging ([#6](https://github.com/evil8io/tailjump/issues/6)) ([a1ff500](https://github.com/evil8io/tailjump/commit/a1ff50083fcb9b1e76e07336f6ed28320b69ecdd))

## [1.1.0](https://github.com/evil8io/tailjump/compare/v1.0.0...v1.1.0) (2026-09-11)


### Features

* **cli:** add --user to every SSH command; rename laptop to client ([#3](https://github.com/evil8io/tailjump/issues/3)) ([f62efbd](https://github.com/evil8io/tailjump/commit/f62efbd8cebf35f35f53af7c353d99d6503cfd74))
* **cli:** add a repeatable --network flag to connect ([#4](https://github.com/evil8io/tailjump/issues/4)) ([d14dba1](https://github.com/evil8io/tailjump/commit/d14dba1204ffae6dd556f2b289c8e1048f47612f))

## 1.0.0 (2026-09-11)


### Features

* tailjump v1, sshuttle-style sessions over Tailscale SSH ([b3c0bd9](https://github.com/evil8io/tailjump/commit/b3c0bd936fcb4784c32ec6db7aa3718adb6058a5))
